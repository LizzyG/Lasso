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

	"github.com/aws/aws-sdk-go/aws"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
)

type dbFile int

const (
	artistsDb dbFile = iota
)

type idType int

const (
	spotifyId idType = iota
)

var idTypeNames = map[idType]string{spotifyId: "SpotifyID"}

var dbFiles = map[dbFile]string{artistsDb: "db/artists.db"}

const configFilePath = "config.ini"

var (
	scopes = []string{
		spotify.ScopePlaylistReadCollaborative,
		spotify.ScopePlaylistReadPrivate,
		spotify.ScopeUserReadPrivate,
		spotify.ScopePlaylistModifyPrivate,
		spotify.ScopeUserFollowRead,
		spotify.ScopeUserTopRead}
	ch      = make(chan *spotify.Client)
	clients = make(map[string]spotify.Client) //really this should be a cache or something
	state   = "abc123"                        //this is a security thing to validate the request actually originated with me.  Do something with it later.
)

var (
	clientID, clientSecret, port, ip, region string
)
var auth spotify.Authenticator
var dynamoClient *dynamodb.DynamoDB

var artists []string

type spotifyInfo struct {
	ID          spotify.ID
	TopTrackIds []spotify.ID
	AsOf        time.Time
}
type artistInfo struct {
	ArtistName  string
	SpotifyInfo spotifyInfo
}

// var Cities = []string{"Portland", "San Francisco"}

func init() {
	ip = os.Getenv("IP")
	if ip == "" {
		ip = "localhost"
	}
	port = os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	redirectURI := "http://" + ip + ":" + port + "/callback"
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
	defer closeDb()

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

	http.HandleFunc("/dumpDb", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got dumpDb request")
		dbDump(artistsDb)
		fmt.Fprint(w, "Dumped")
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
				Cities  []string
			}{userIDCookie.Value, today, maxDate, getSupportedCities()})
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
		log.Printf("failed to get playlists, will move on to create: %v", err)
		//log.Fatalf("couldn't get playlists for user: %v", err)
	} else if deleteExisting {
		for _, p := range myPlaylists.Playlists {
			if p.Name == "local shows" {
				//if deleteExisting {
				unfollowErr := client.UnfollowPlaylist(spotify.ID(p.Owner.ID), p.ID)
				if unfollowErr != nil {
					log.Printf("failed to unfollow: %v", unfollowErr)
				} else {
					log.Println("unfollowed the existing playlist")
				}
				// } else {
				// 	exists = true
				// }
			}
		}
	}
	//if !exists {
	date := time.Now().Format("2006-01-02 15:04:05")
	desc := fmt.Sprintf("local shows - created %v for dates %v - %v", date, startDate, endDate)
	playlist, err := client.CreatePlaylistForUser(userID, "local shows", desc, false)
	if err != nil {
		log.Printf("Encountered an error creating the playlist: %v", err)
	} else {
		log.Println("Created playlist")
	}
	playlistID = playlist.ID
	//}
	return playlistID
}

func completeAuth(ctx context.Context, w http.ResponseWriter, r *http.Request) *spotify.Client {
	tok, err := auth.Token(state, r)
	if err != nil {
		//http.Error(w, "Couldn't get token", http.StatusForbidden)
		log.Printf("auth error: %v", err)
		fmt.Fprint(w, "There was an error while authenticating, please try again later.")
		return nil
	}
	if st := r.FormValue("state"); st != state {
		//http.NotFound(w, r)
		//log.Fatalf("State mismatch: %s != %s\n", st, state)
		log.Printf("auth error: %v", err)
		fmt.Fprint(w, "There was an error in the provided state, please try again later.")
		return nil
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

func doClientCredsAuth() spotify.Client {
	config := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     spotify.TokenURL,
	}
	token, err := config.Token(context.Background())
	if err != nil {
		log.Printf("couldn't get token: %v", err)
		//return nil
	}
	client := spotify.Authenticator{}.NewClient(token)
	return client
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
			case "AWS_REGION":
				region = strings.TrimSpace(parts[1])
				os.Setenv("AWS_REGION", region)
			}
		}
	}
	err = s.Err()
	if err != nil {
		log.Fatal(err)
	}
	if region == "" {
		region = "us-west-2"
	}
	auth.SetAuthInfo(clientID, clientSecret)
}

//SpotifySearch searches for a list of artists on Spotify, and if a sufficient match is
//found, then that artist's top tracks are added to the playlist using the AddTopArtistTracks method.
func spotifySearch(wg *sync.WaitGroup, client *spotify.Client, artist string, playlistID spotify.ID) {
	defer wg.Done()
	var artistID spotify.ID
	//first check the db for the artists id

	//artistID = spotify.ID(getArtistID(artist, spotifyId))
	info := getArtistInfoFromDb(artist)
	artistID = info.SpotifyInfo.ID
	//if it wasn't saved, then call spotify search
	if artistID == "" {
		result, err := client.Search(artist, spotify.SearchTypeArtist)
		if err != nil {
			log.Printf("Encountered an error searching for artist %v: %v", artist, err)
			return
		} else if result.Artists.Total > 0 {
			for i, match := range result.Artists.Artists {
				if match.Name == artist {
					artistID = result.Artists.Artists[i].ID
					//dbInsert(artistsDb, artist, artistID.String())
					//setArtistID(artist, artistID.String(), spotifyId)
				}
			}
		}
	}

	if artistID != "" {
		//check if we had the tracks saved and when they were last retrieved
		savedTracks := info.SpotifyInfo.TopTrackIds

		//if we didn't have anything saved, or what was saved is old, do the spotify lookup
		var trackIds []spotify.ID
		if len(savedTracks) == 0 || time.Now().Sub(info.SpotifyInfo.AsOf) > time.Hour*24 {
			tracks, err := client.GetArtistsTopTracks(artistID, spotify.CountryUSA)
			//if we get an error then log if and use the old info, if there was any
			if err != nil {
				log.Printf("Encountered an error getting tracks for artist %v: %v", artistID, err)
				trackIds = savedTracks
			} else {
				trackIds := make([]spotify.ID, len(tracks))
				for i, track := range tracks {
					trackIds[i] = track.SimpleTrack.ID
				}
				//add new info to db
				info.ArtistName = artist
				info.SpotifyInfo = spotifyInfo{ID: artistID, TopTrackIds: trackIds, AsOf: time.Now()}
				setArtistInfo(artist, info)
			}
		}
		if len(trackIds) == 0 && len(savedTracks) > 0 {
			trackIds = savedTracks
		}
		if len(trackIds) > 0 {
			_, err := client.AddTracksToPlaylist(playlistID, trackIds...)
			if err != nil {

				log.Printf("Error adding tracks for artist %v: %v", artist, err)
			}
		}
	} else {
		log.Printf("Could not locate artist %v ", artist)
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
		log.Printf("no client for the user %v", userIDCookie.Value) //really redirect to login
	}
	//get the form data
	deleteExisting := r.FormValue("deleteExisting")
	startStr := r.FormValue("start")
	endStr := r.FormValue("end")
	startDate, startErr := time.Parse("2006-01-02", startStr)
	if startErr != nil {
		log.Printf("Error parsing start date: %v", startErr)
	} else {
		log.Printf("start: %v", startDate)
	}
	endDate, endErr := time.Parse("2006-01-02", endStr)
	if endErr != nil {
		log.Printf("Error parsing start date: %v", endErr)
	} else {
		log.Printf("end: %v", endDate)
	}

	city := r.FormValue("city")

	playlistID := setupPlaylist(&userClient, userIDCookie.Value, startStr, endStr, deleteExisting == "on")
	if playlistID == "" {
		fmt.Fprint(w, "There was an error creating the playlist, please try again later.")
		return
	}
	artists, err = scrapeDates(startDate, endDate, city)
	if err != nil && len(artists) == 0 {
		w.WriteHeader(500)
		fmt.Fprint(w, "There was an error retrieving events, please try again later.")
		return
	}
	if len(artists) == 0 {
		fmt.Fprint(w, "We didn't find any artists in your date range.")
		return
	}
	ch := make(chan []string)
	go getLikedIntersect(&userClient, artists, ch)
	likedShows := <-ch
	close(ch)
	var wg sync.WaitGroup
	log.Printf("found %v artists", len(artists))
	for _, artist := range artists {
		artist = strings.TrimSpace(artist)
		wg.Add(1)
		go spotifySearch(&wg, &userClient, artist, playlistID)
	}
	wg.Wait()
	msg := "Done, go check out your cool new playlist"
	if err != nil {
		msg = msg + ", but there may be some artists missing because we encountered an error"
	}
	if len(likedShows) > 0 {
		//fmt.Fprintf(w, "Done making the playlist, there are some shows you might like: %v", likedShows)
		msg = fmt.Sprintf("%v. \n There are some shows you might like: %v", msg, likedShows)
	}
	// } else {
	// 	fmt.Fprint(w, "Done, go check out your cool new playlist")
	// }
	//fmt.Fprint(w, "Done, go check out your cool new playlist")43RE3R
	fmt.Fprintf(w, msg)
}

func getLikedIntersect(client *spotify.Client, artists []string, ch chan []string) {
	liked := getLikedArtists(client)
	log.Printf("Found %v liked artists", len(liked))
	intersect := intersect(artists, liked)
	ch <- intersect
}

func getUserClient(userID string) (spotify.Client, error) {
	client, found := clients[userID]
	var err error
	if !found {
		err = errors.New("No client for this user")
	}
	return client, err
}

//This isn't really necessary - slowpoke will automatically create the files on demand
//Using it as a placeholder for when I switch to a different db that might need initialization
func setupDB() {
	// for dbName, file := range dbFiles {
	// 	log.Printf("Initializing %v db file", dbName)
	// 	slowpoke.Set(file, []byte("testKeyStr"), []byte("testVal"))
	// }
	config := &aws.Config{Region: &region}
	sess := session.Must(session.NewSession(config))
	dynamoClient = dynamodb.New(sess)
}

func closeDb() {
	slowpoke.CloseAll()
}

// func dbInsert(db dbFile, KeyStr string, val string) {
// 	slowpoke.Set(dbFiles[db], []byte(KeyStr), []byte(val))
// }

func getArtistID(artistName string, idType idType) string {
	res, err := dynamoClient.GetItem(&dynamodb.GetItemInput{TableName: aws.String("ArtistIDs"), Key: map[string]*dynamodb.AttributeValue{"ArtistName": {S: aws.String(artistName)}}})
	if err != nil {
		panic(err)
	}
	item := res.Item
	idVal, exists := item[idTypeNames[idType]]
	if exists {
		return idVal.String()
	}
	return ""
}

func getArtistInfoFromDb(artistName string) *artistInfo {
	res, err := dynamoClient.GetItem(&dynamodb.GetItemInput{TableName: aws.String("ArtistInfo"), Key: map[string]*dynamodb.AttributeValue{"ArtistName": {S: aws.String(artistName)}}})
	if err != nil {
		//panic(err)
		log.Println("error getting artist info:", err)
	}
	info := artistInfo{}
	dynamodbattribute.UnmarshalMap(res.Item, &info)
	return &info
}

func setArtistInfo(artistName string, info *artistInfo) {
	val, err := dynamodbattribute.Marshal(info)
	if err != nil {
		log.Printf("Error marshalling artist info for artist %v: %v", artistName, err)
	}
	goodVal := map[string]*dynamodb.AttributeValue{":spotifyInfo": val.M["SpotifyInfo"]}
	expr := "set spotifyInfo=:spotifyInfo"
	put := dynamodb.UpdateItemInput{TableName: aws.String("ArtistInfo"),
		Key:                       map[string]*dynamodb.AttributeValue{"ArtistName": {S: aws.String(artistName)}},
		ExpressionAttributeValues: goodVal,
		UpdateExpression:          aws.String(expr)}
	_, err = dynamoClient.UpdateItem(&put)
	if err != nil {
		log.Printf("error adding artist info to db: %v", err)
	}
}

func setArtistID(artistName string, artistID string, idType idType) {
	put := dynamodb.UpdateItemInput{TableName: aws.String("ArtistIDs"),
		Key: map[string]*dynamodb.AttributeValue{"ArtistName": {S: aws.String(artistName)},
			idTypeNames[idType]: {S: aws.String(artistID)}}}
	dynamoClient.UpdateItem(&put)
}

// func dbGet(db dbFile, KeyStr string) string {
// 	val, err := slowpoke.Get(dbFiles[db], []byte(KeyStr))
// 	if err != nil {
// 		return ""
// 	}
// 	return string(val)
// }

func dbDump(db dbFile) {
	KeyStrs, err := slowpoke.Keys(dbFiles[db], nil, 0, 0, true)
	if err != nil {
		log.Printf("Error dumping db: %v", err)
		return
	}
	for _, KeyStr := range KeyStrs {
		artistBytes, _ := slowpoke.Get(dbFiles[db], KeyStr)
		log.Println(string(KeyStr), string(artistBytes))
	}
}

func getLikedArtists(client *spotify.Client) []string {
	liked := make(map[string]bool)
	//get explicitly liked artists
	res, err := client.CurrentUsersFollowedArtists()
	if err != nil {
		log.Printf("Encountered an error getting followed artists: %v", err)
		//return liked
	} else {
		for _, artist := range res.Artists {
			liked[artist.Name] = true
		}
	}

	//get top played artists
	top, err := client.CurrentUsersTopArtists()
	if err != nil {
		log.Printf("Encountered an error getting top artists: %v", err)
		//return liked
	} else {
		for _, artist := range top.Artists {
			liked[artist.Name] = true
		}
	}
	ret := make([]string, len(liked))
	i := 0
	for val := range liked {
		ret[i] = val
		i++
	}
	return ret
}

func intersect(arists, liked []string) (c []string) {
	m := make(map[string]bool)

	for _, item := range arists {
		m[item] = true
	}

	for _, item := range liked {
		if _, ok := m[item]; ok {
			c = append(c, item)
		}
	}
	return
}

//client can be generic application client, not user specific
func getSetSpotifyInfo(artist string, spotClient *spotify.Client) {
	var artistID spotify.ID
	result, err := spotClient.Search(artist, spotify.SearchTypeArtist)
	if err != nil {
		log.Printf("Encountered an error searching for artist %v: %v", artist, err)
		return
	} else if result.Artists.Total > 0 {
		for i, match := range result.Artists.Artists {
			if match.Name == artist {
				artistID = result.Artists.Artists[i].ID
			}
		}
	}

	//still record that there is no info for this artist so we don't keep looking for it
	if artistID == "" {
		info := artistInfo{ArtistName: artist, SpotifyInfo: spotifyInfo{ID: "", TopTrackIds: nil, AsOf: time.Now()}}
		setArtistInfo(artist, &info)
		return
	}

	tracks, err := spotClient.GetArtistsTopTracks(artistID, spotify.CountryUSA)
	//if we get an error then log if and use the old info, if there was any
	if err != nil {
		log.Printf("Encountered an error getting tracks for artist %v: %v", artistID, err)
		return
	}

	trackIds := make([]spotify.ID, len(tracks))
	for i, track := range tracks {
		trackIds[i] = track.SimpleTrack.ID
	}
	//add new info to db
	info := artistInfo{ArtistName: artist, SpotifyInfo: spotifyInfo{ID: artistID, TopTrackIds: trackIds, AsOf: time.Now()}}
	setArtistInfo(artist, &info)
}
