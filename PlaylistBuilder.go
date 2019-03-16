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
	"time"

	"github.com/gocolly/colly"
	"github.com/recoilme/slowpoke"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2/clientcredentials"
)

const dbFile = "test/example.db"

//const redirectURI = "https://example.com/callback/"
const redirectURI = "http://localhost:8080/callback"
const configFilePath = "C:\\Users\\lizga\\repos\\PlaylistBuilder\\config.ini"

var (
	scopes  = []string{spotify.ScopePlaylistReadCollaborative, spotify.ScopePlaylistReadPrivate, spotify.ScopeUserReadPrivate, spotify.ScopePlaylistModifyPrivate}
	auth    = spotify.NewAuthenticator(redirectURI, scopes...)
	ch      = make(chan *spotify.Client)
	clients = make(map[string]spotify.Client) //really this should be a cache or something
	state   = "abc123"                        //this is a security thing to validate the request actually originated with me.  Do something with it later.
)

var (
	clientID, clientSecret string
)

var artists []string

func main() {

	//set up our log file
	f, err := os.OpenFile("testlogfile.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)
	setupCredentials()
	setupDB()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got request for:", r.URL.String())
		homeHandler(w, r)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		log.Printf("callback request: %v", r)
		client := completeAuth(ctx, w, r)
		log.Println("callback got the client")
		ch <- client
	})

	http.HandleFunc("/managePlaylist", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got a request to maange the playlist")
		//t, _ := template.ParseFiles("templates/manage.html")
		//t.Execute(w, nil)
		manageHandler(w, r)
	})

	http.HandleFunc("/makePlaylist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			log.Printf("makePlaylist got a non-post request: %v", r.Method)
			fmt.Fprint(w, "This call only allows post requests")
			return
		}
		log.Println("Got a request to build the playlist")
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
		//ScrapeMerc("", 0)
		artists = scrapeDates(startDate, endDate)
		SpotifySearch(&userClient, artists, playlistID)
	})
	http.HandleFunc("/blarg", func(w http.ResponseWriter, r *http.Request) {
		client := <-ch
		user, _ := client.CurrentUser()
		fmt.Fprintf(w, "hello %v", user.ID)
	})

	http.ListenAndServe(":8080", nil)

}

func manageHandler(w http.ResponseWriter, r *http.Request) {
	if userIDCookie, _ := r.Cookie("user_id"); userIDCookie != nil {

		if _, ok := clients[userIDCookie.Value]; ok {
			today := time.Now().Format("2006-01-02")
			maxDate := time.Now().Add(time.Hour * 168).Format("2006-01-02")
			t, err := template.ParseFiles("templates/manage.html")
			if err != nil {
				log.Printf("err parsing template: %v", err)
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
	// userIDCookie, _ := r.Cookie("user_id")
	// if userIDCookie != nil {
	// 	client := clients[userIDCookie.Value]
	// }
	if userIDCookie, _ := r.Cookie("user_id"); userIDCookie != nil {

		if _, ok := clients[userIDCookie.Value]; ok {
			http.Redirect(w, r, "/managePlaylist", 302)
			return
			//manageHandler(w, r)

		} else {
			log.Printf("no client found for this user: %v", userIDCookie.Value)
		}
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
				log.Println(p.Name, p.Owner, p.ID)
				unfollowErr := client.UnfollowPlaylist(spotify.ID(p.Owner.ID), p.ID)
				if unfollowErr != nil {
					fmt.Printf("failed to unfollow: %v", unfollowErr)
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

//ScrapeMerc scrapes the Portland Mercury music listings for shows on the specifed date.
func ScrapeMerc(dateReq string, page int) {
	//Mon Jan 2 15:04:05 MST 2006
	if page <= 1 {
		page = 1
	}
	var date string
	if len(dateReq) == 0 {
		date = time.Now().Format("2006-01-02")
	} else {
		_, err := time.Parse("2006-01-02", dateReq) //validate the requested date by parsing it
		if err == nil {
			date = dateReq
		} else {
			log.Fatalf("Could not parse requested date %v: ", dateReq)
			return
		}

	}

	site := "https://www.portlandmercury.com/events/music/" + date

	c := colly.NewCollector()

	c.OnHTML("h3.calendar-post-title>a", func(e *colly.HTMLElement) {
		text := strings.Trim(e.Text, "\n ")
		//log.Println(text)

		artists = append(artists, strings.Split(text, ",")...)
	})
	c.OnHTML("li.next>a", func(e *colly.HTMLElement) {
		if strings.Contains(e.Text, "Next on") {
			log.Println("there's another page")
			page = page + 1
			site = fmt.Sprintf(site+"?page=%v&view_id=events", page)

			log.Println(site)
			c.Visit(site)
		}

	})

	c.Visit(site)
	return
}

//SpotifySearch searches for a list of artists on Spotify, and if a sufficient match is
//found, then that artist's top tracks are added to the playlist using the AddTopArtistTracks method.
func SpotifySearch(client *spotify.Client, artists []string, playlistID spotify.ID) {
	for _, artist := range artists {
		artist = strings.TrimSpace(artist)
		result, err := client.Search(artist, spotify.SearchTypeArtist)
		if err != nil {
			log.Printf("Encountered an error search for artist %v: %v", artist, err)
		} else if result.Artists.Total > 1 {
			log.Printf("Found multiple matches for artist %v", artist)
			for i, match := range result.Artists.Artists {
				//log.Println(match.Name)
				if match.Name == artist {
					log.Printf("Using exact match %v for artist %v", match.Name, artist)
					//tracks, err := client.GetArtistsTopTracks(result.Artists.Artists[i].ID, spotify.CountryUSA)
					AddTopArtistTracks(client, playlistID, result.Artists.Artists[i].ID)
					break
				} else {
					log.Printf("Rejecting potential match %v for artist %v", match.Name, artist)
				}
			}
		} else if result.Artists.Total == 0 {
			log.Printf("Could not locate artist %v ", artist)
		} else {
			//log.Printf("adding tracks to playlist for artist %v", artist)
			AddTopArtistTracks(client, playlistID, result.Artists.Artists[0].ID)
		}
	}
}

//AddTopArtistTracks adds the top tracks for the specified artist to the specified playlist.
func AddTopArtistTracks(client *spotify.Client, playlistID spotify.ID, artistID spotify.ID) {
	tracks, err := client.GetArtistsTopTracks(artistID, spotify.CountryUSA)
	if err != nil {
		log.Printf("Encountered an error getting tracks for artist %v: %v", artistID, err)
	}
	for i := 0; i < len(tracks); i++ {

		//trackIds[i] = tracks[i].SimpleTrack.ID
		client.AddTracksToPlaylist(playlistID, tracks[i].SimpleTrack.ID)
	}
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
