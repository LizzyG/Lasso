package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/recoilme/slowpoke"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2/clientcredentials"
)

const dbFile = "test/example.db"

//const redirectURI = "https://example.com/callback/"
//const redirectURI = "http://localhost:" + port + "/callback"
const configFilePath = "config.ini"

var (
	scopes  = []string{spotify.ScopePlaylistReadCollaborative, spotify.ScopePlaylistReadPrivate, spotify.ScopeUserReadPrivate, spotify.ScopePlaylistModifyPrivate}
	ch      = make(chan *spotify.Client)
	clients = make(map[string]spotify.Client) //really this should be a cache or something
	state   = "abc123"                        //this is a security thing to validate the request actually originated with me.  Do something with it later.
)

var (
	clientID, clientSecret, port string
)
var auth spotify.Authenticator

var artists []string

func init() {
	port = os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	redirectURI := "http://localhost:" + port + "/callback"
	auth = spotify.NewAuthenticator(redirectURI, scopes...)
}

func main() {
	//set up our log file
	f, err := os.OpenFile("lasso.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)
	log.Printf("using port %v", port)
	log.Printf("using redirect url %v", "http://localhost:"+port+"/callback")
	setupCredentials()
	setupDB()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got request for:", r.URL.String())
		homeHandler(w, r)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		client := completeAuth(ctx, w, r)
		ch <- client
	})

	http.HandleFunc("/managePlaylist", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got a request to maange the playlist")
		manageHandler(w, r)
	})

	http.HandleFunc("/makePlaylist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			log.Printf("makePlaylist got a non-post request: %v", r.Method)
			fmt.Fprint(w, "This call only allows post requests")
			return
		}
		log.Println("Got a request to build the playlist")
		makePlaylistHandler(w, r)
	})

	http.HandleFunc("/blarg", func(w http.ResponseWriter, r *http.Request) {
		client := <-ch
		user, _ := client.CurrentUser()
		fmt.Fprintf(w, "hello %v", user.ID)
	})
	http.ListenAndServe(":"+port, nil)
}

func manageHandler(w http.ResponseWriter, r *http.Request) {
	if userIDCookie, _ := r.Cookie("user_id"); userIDCookie != nil {

		if _, ok := clients[userIDCookie.Value]; ok {
			today := time.Now().Format("2006-01-02")
			maxDate := time.Now().Add(time.Hour * 168).Format("2006-01-02")
			t, err := template.ParseFiles("templates/manage.html")
			if err != nil {
				log.Printf("err parsing template: %v", err)
				w.WriteHeader(500)
				fmt.Fprint(w, "there was an error, please try your request again")
			}
			t.Execute(w, struct {
				UserID  string
				Today   string
				MaxDate string
			}{userIDCookie.Value, today, maxDate})
		} else {
			fmt.Fprintf(w, "no client found for this user: %v", userIDCookie.Value)
		}
	} else {
		fmt.Fprint(w, "no userId cookie on this request")
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if userIDCookie, _ := r.Cookie("user_id"); userIDCookie != nil {

		if _, ok := clients[userIDCookie.Value]; ok {
			http.Redirect(w, r, "/managePlaylist", 302)
			return
		}
		log.Printf("no client found for this user: %v", userIDCookie.Value)
	} else {
		log.Print("no userId cookie on this request")
	}
	url := auth.AuthURL(state)
	t, _ := template.ParseFiles("templates/index.html")
	t.Execute(w, struct{ URL string }{url})
}

func setupPlaylist(client *spotify.Client, userID string, startDate string, endDate string, deleteExisting bool) spotify.ID {
	var playlistID spotify.ID
	myPlaylists, err := client.GetPlaylistsForUser(userID)
	if err != nil {
		fmt.Printf("failed to get playlists: %v", err)
		log.Fatalf("couldn't get playlists for user: %v", err)
	}
	var exists = false
	for _, p := range myPlaylists.Playlists {
		if p.Name == "local shows" {
			if deleteExisting {
				unfollowErr := client.UnfollowPlaylist(spotify.ID(p.Owner.ID), p.ID)
				if unfollowErr != nil {
					log.Printf("failed to unfollow: %v", unfollowErr)
				} else {
					log.Println("unfollowed the existing playlist")
				}
			} else {
				exists = true
			}
		}
	}
	if !exists {
		date := time.Now().Format("2006-01-02 15:04:05")
		desc := fmt.Sprintf("local shows - created %v for dates %v - %v", date, startDate, endDate)
		playlist, err := client.CreatePlaylistForUser(userID, "local shows", desc, false)
		if err != nil {
			log.Fatalf("Encountered an error creating the playlist: %v", err)
		} else {
			log.Println("Created playlist")
		}
		playlistID = playlist.ID
	}
	return playlistID
}

func completeAuth(ctx context.Context, w http.ResponseWriter, r *http.Request) *spotify.Client {
	tok, err := auth.Token(state, r)
	if err != nil {
		http.Error(w, "Couldn't get token", http.StatusForbidden)
		log.Fatal(err)
	}
	if st := r.FormValue("state"); st != state {
		http.NotFound(w, r)
		log.Fatalf("State mismatch: %s != %s\n", st, state)
	}
	// use the token to get an authenticated client
	client := auth.NewClient(tok)
	client.AutoRetry = true
	user, _ := client.CurrentUser()
	log.Printf("user id is %v", user.ID)
	http.SetCookie(w, &http.Cookie{
		Name:    "user_id",
		Value:   user.ID,
		Expires: time.Now().Add(120 * time.Minute),
	})
	clients[user.ID] = client
	fmt.Fprintf(w, "you are user %v", user.ID)
	return &client
}

//this isn't used right now but I'm going to leave it in case
//we want to do some non-user specific lookups at some point
func doClientCredsAuth() spotify.Client {
	config := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     spotify.TokenURL,
	}
	token, err := config.Token(context.Background())
	if err != nil {
		log.Fatalf("couldn't get token: %v", err)
	}

	return spotify.Authenticator{}.NewClient(token)
}

func setupCredentials() {

	f, err := os.Open(configFilePath)
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		parts := strings.Split(line, "=")
		if len(parts) == 2 {
			switch strings.TrimSpace(parts[0]) {
			case "CLIENT_ID":
				clientID = strings.TrimSpace(parts[1])
			case "CLIENT_SECRET":
				clientSecret = strings.TrimSpace(parts[1])
			}
		}
	}
	err = s.Err()
	if err != nil {
		log.Fatal(err)
	}
	auth.SetAuthInfo(clientID, clientSecret)
}

//SpotifySearch searches for a list of artists on Spotify, and if a sufficient match is
//found, then that artist's top tracks are added to the playlist using the AddTopArtistTracks method.
func spotifySearch(wg *sync.WaitGroup, client *spotify.Client, artist string, playlistID spotify.ID) {
	defer wg.Done()
	var artistID spotify.ID
	result, err := client.Search(artist, spotify.SearchTypeArtist)
	if err != nil {
		log.Printf("Encountered an error searching for artist %v: %v", artist, err)
		return
	} else if result.Artists.Total > 1 {
		for i, match := range result.Artists.Artists {
			if match.Name == artist {
				artistID = result.Artists.Artists[i].ID
				break
			}
		}
	} else if result.Artists.Total == 0 {
		log.Printf("Could not locate artist %v ", artist)
		return
	} else {
		artistID = result.Artists.Artists[0].ID
	}
	if artistID != "" {
		tracks, err := client.GetArtistsTopTracks(artistID, spotify.CountryUSA)
		if err != nil {
			log.Printf("Encountered an error getting tracks for artist %v: %v", artistID, err)

		} else {
			trackIds := make([]spotify.ID, len(tracks))
			for i, track := range tracks {
				trackIds[i] = track.SimpleTrack.ID
			}
			client.AddTracksToPlaylist(playlistID, trackIds...)
		}
	}
}

func makePlaylistHandler(w http.ResponseWriter, r *http.Request) {
	userIDCookie, cookieErr := r.Cookie("user_id")
	if cookieErr != nil {
		log.Printf("Failed to get user id cookie: %v", cookieErr)
		fmt.Fprintf(w, "failed to get user") //really redirect to login here
	}
	log.Printf("Got userID from cookie: %v", userIDCookie.Value)
	userClient, err := getUserClient(userIDCookie.Value)
	if err != nil {
		log.Fatalf("no client for the user") //really redirect to login
	}
	//get the form data
	deleteExisting := r.FormValue("deleteExisting")
	startStr := r.FormValue("start")
	endStr := r.FormValue("end")
	startDate, _ := time.Parse("2006-01-02", startStr)
	endDate, _ := time.Parse("2006-01-02", endStr)

	playlistID := setupPlaylist(&userClient, userIDCookie.Value, startStr, endStr, deleteExisting == "on")
	artists = scrapeDates(startDate, endDate)
	var wg sync.WaitGroup
	log.Printf("found %v artists", len(artists))
	for _, artist := range artists {
		artist = strings.TrimSpace(artist)
		wg.Add(1)
		go spotifySearch(&wg, &userClient, artist, playlistID)
	}
	wg.Wait()
	fmt.Fprint(w, "Done, go check out your cool new playlist")
}

func getUserClient(userID string) (spotify.Client, error) {
	client, found := clients[userID]
	var err error
	if !found {
		err = errors.New("No client for this user")
	}
	return client, err
}

func setupDB() {
	defer slowpoke.CloseAll()
	slowpoke.Set(dbFile, []byte("testKey"), []byte("testVal"))
}
