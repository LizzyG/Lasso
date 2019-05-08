package media

import (
	"context"
	"errors"
	"fmt"
	"lasso/internal/pkg/scrape"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/zmb3/spotify"
)

var (
	scopes = []string{
		spotify.ScopePlaylistReadCollaborative,
		spotify.ScopePlaylistReadPrivate,
		spotify.ScopeUserReadPrivate,
		spotify.ScopePlaylistModifyPrivate,
		spotify.ScopeUserFollowRead,
		spotify.ScopeUserTopRead}

	clients = make(map[string]spotify.Client) //really this should be a cache or something
	state   = "abc123"                        //this is a security thing to validate the request actually originated with me.  Do something with it later.
)
var spotAuth spotify.Authenticator

type SpotifyInfo struct {
	ID          spotify.ID
	TopTrackIds []spotify.ID
	AsOf        time.Time
}

func Setup(clientID string, clientSecret string, ip string, port string) {
	redirectURI := "http://" + ip + ":" + port + "/callback"
	spotAuth = spotify.NewAuthenticator(redirectURI, scopes...)
	spotAuth.SetAuthInfo(clientID, clientSecret)
}

func CompleteAuth(ctx context.Context, w http.ResponseWriter, r *http.Request) *spotify.Client {
	tok, err := spotAuth.Token(state, r)
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
	client := spotAuth.NewClient(tok)
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

func ManagePlaylist(userID string) error {
	if _, ok := clients[userID]; ok {

		return nil

	} else {
		return errors.New("no client found for this user")
	}
}

func MakePlaylist(dynamoClient *dynamodb.DynamoDB, userID string, startDate time.Time, endDate time.Time, deleteExisting bool, city string) string {
	userClient, err := getUserClient(userID)
	playlistID := setupPlaylist(&userClient, userID, startDate, endDate, deleteExisting)
	if playlistID == "" {
		return "There was an error creating the playlist, please try again later."
	}

	artists, err := scrape.ScrapeDates(dynamoClient, startDate, endDate, city)
	if err != nil && len(artists) == 0 {
		//w.WriteHeader(500)
		return "There was an error retrieving events, please try again later."
	}
	if len(artists) == 0 {
		return "We didn't find any artists in your date range."
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
		go spotifySearch(&wg, &userClient, artist, playlistID, dynamoClient)
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
	return msg

}

func setupPlaylist(client *spotify.Client, userID string, startDate time.Time, endDate time.Time, deleteExisting bool) spotify.ID {
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

func getUserClient(userID string) (spotify.Client, error) {
	client, found := clients[userID]
	var err error
	if !found {
		err = errors.New("No client for this user")
	}
	return client, err
}

func IsLoggedIn(userID string) (bool, string) {
	if _, ok := clients[userID]; ok {
		return true, ""
	}
	return false, spotAuth.AuthURL(state)
}

//SpotifySearch searches for a list of artists on Spotify, and if a sufficient match is
//found, then that artist's top tracks are added to the playlist using the AddTopArtistTracks method.
func spotifySearch(wg *sync.WaitGroup, client *spotify.Client, artist string, playlistID spotify.ID, dynamoClient *dynamodb.DynamoDB) {
	defer wg.Done()
	var artistID spotify.ID
	//first check the db for the artists id

	//artistID = spotify.ID(getArtistID(artist, spotifyId))
	info := getArtistInfoFromDb(dynamoClient, artist)
	artistID = info.ID
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
		savedTracks := info.TopTrackIds

		//if we didn't have anything saved, or what was saved is old, do the spotify lookup
		var trackIds []spotify.ID
		if len(savedTracks) == 0 || time.Now().Sub(info.AsOf) > time.Hour*24 {
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
				info = &SpotifyInfo{ID: artistID, TopTrackIds: trackIds, AsOf: time.Now()}
				setArtistInfo(dynamoClient, artist, info)
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

func getLikedIntersect(client *spotify.Client, artists []string, ch chan []string) {
	liked := getLikedArtists(client)
	log.Printf("Found %v liked artists", len(liked))
	intersect := intersect(artists, liked)
	ch <- intersect
}

// func getArtistID(artistName string, idType idType) string {
// 	res, err := dynamoClient.GetItem(&dynamodb.GetItemInput{TableName: aws.String("ArtistIDs"), Key: map[string]*dynamodb.AttributeValue{"ArtistName": {S: aws.String(artistName)}}})
// 	if err != nil {
// 		panic(err)
// 	}
// 	item := res.Item
// 	idVal, exists := item[idTypeNames[idType]]
// 	if exists {
// 		return idVal.String()
// 	}
// 	return ""
// }

func getArtistInfoFromDb(dynamoClient *dynamodb.DynamoDB, artistName string) *SpotifyInfo {
	res, err := dynamoClient.GetItem(&dynamodb.GetItemInput{TableName: aws.String("ArtistInfo"),
		Key: map[string]*dynamodb.AttributeValue{"ArtistName": {S: aws.String(artistName)}}, ProjectionExpression: aws.String("spotifyInfo")})
	if err != nil {
		//panic(err)
		log.Println("error getting artist info:", err)
	}
	info := SpotifyInfo{}
	err = dynamodbattribute.Unmarshal(res.Item["spotifyInfo"], &info)
	if err != nil {
		log.Println("error unmarshalling artist info")
	}
	return &info
}

func setArtistInfo(dynamoClient *dynamodb.DynamoDB, artistName string, info *SpotifyInfo) {
	val, err := dynamodbattribute.Marshal(info)
	if err != nil {
		log.Printf("Error marshalling artist info for artist %v: %v", artistName, err)
	}
	goodVal := map[string]*dynamodb.AttributeValue{":spotifyInfo": val}
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

// func setArtistID(artistName string, artistID string, idType idType) {
// 	put := dynamodb.UpdateItemInput{TableName: aws.String("ArtistIDs"),
// 		Key: map[string]*dynamodb.AttributeValue{"ArtistName": {S: aws.String(artistName)},
// 			idTypeNames[idType]: {S: aws.String(artistID)}}}
// 	dynamoClient.UpdateItem(&put)
// }

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
func getSetSpotifyInfo(artist string, spotClient *spotify.Client, dynamoClient *dynamodb.DynamoDB) {
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
		info := SpotifyInfo{ID: "", TopTrackIds: nil, AsOf: time.Now()}
		setArtistInfo(dynamoClient, artist, &info)
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
	info := SpotifyInfo{ID: artistID, TopTrackIds: trackIds, AsOf: time.Now()}
	setArtistInfo(dynamoClient, artist, &info)
}
