package main

import (
	"bufio"
	"context"
	"fmt"
	"html/template"
	"lasso/internal/pkg/media"
	"lasso/internal/pkg/scrape"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zmb3/spotify"

	"github.com/aws/aws-sdk-go/aws"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
)

type idType int

const (
	spotifyId idType = iota
)

var ch = make(chan *spotify.Client)
var idTypeNames = map[idType]string{spotifyId: "SpotifyID"}

const configFilePath = "configs/config.ini"

var (
	ip, port, clientID, clientSecret, region string
)

var dynamoClient *dynamodb.DynamoDB

type artistInfo struct {
	ArtistName  string
	SpotifyInfo media.SpotifyInfo
}

type preScrapeOptions struct {
	daysOut          int
	eventCacheHours  int
	pdxOnly          bool
	artistCacheHours int
	timeoutMinutes   int
}

func init() {
	ip = os.Getenv("IP")
	if ip == "" {
		ip = "localhost"
	}
	port = os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
}

func main() {
	//set up our log file
	f, err := os.OpenFile("logs/lasso.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
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
		if r.URL.Path != "/" {
			return
		}
		homeHandler(w, r)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		client := media.CompleteAuth(ctx, w, r)
		ch <- client
	})

	http.HandleFunc("/managePlaylist", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got a request to manage the playlist")
		path := r.URL.Path
		log.Println(path)
		manageHandler(w, r)
	})

	http.HandleFunc("/makePlaylist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			log.Printf("makePlaylist got a non-post request: %v", r.Method)
			fmt.Fprint(w, "This call only allows post requests")
			return
		}

		ctx := r.Context
		log.Printf("makePlaylist context: %v", ctx)
		log.Println("Got a request to build the playlist")
		path := r.URL.Path
		log.Println(path)
		makePlaylistHandler(w, r)
	})

	http.HandleFunc("/blarg", func(w http.ResponseWriter, r *http.Request) {
		client := <-ch
		user, _ := client.CurrentUser()
		fmt.Fprintf(w, "hello %v", user.ID)
	})

	http.HandleFunc("/preScrape", func(w http.ResponseWriter, r *http.Request) {
		log.Println("got a request for /preScrape")
		opt := getPreScrapeOptions()
		artists := scrape.PreScrape(dynamoClient, opt.daysOut, opt.eventCacheHours, opt.timeoutMinutes, opt.pdxOnly)
		media.PreSearch(dynamoClient, artists, 48)

	})
	http.ListenAndServe(":"+port, nil)
}

func manageHandler(w http.ResponseWriter, r *http.Request) {
	if userIDCookie, _ := r.Cookie("user_id"); userIDCookie != nil {

		err := media.ManagePlaylist(userIDCookie.Value)
		if err == nil {
			today :=time.Now().Add(time.Hour * -12).Format("2006-01-02")
			//today := time.Now().Format("2006-01-02")
			maxDate := time.Now().Add(time.Hour * 168).Format("2006-01-02")
			t, err := template.ParseFiles("web/manage.html")
			if err != nil {
				log.Printf("err parsing template: %v", err)
				//w.WriteHeader(500)
				fmt.Fprint(w, "there was an error, please try your request again")
			}
			t.Execute(w, struct {
				UserID  string
				Today   string
				MaxDate string
				Cities  []string
			}{userIDCookie.Value, today, maxDate, scrape.GetSupportedCities()})
		} else {
			fmt.Fprint(w, err)
		}
	} else {
		fmt.Fprint(w, "no userId cookie on this request")
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	userID := ""
	if userIDCookie, _ := r.Cookie("user_id"); userIDCookie != nil {
		userID = userIDCookie.Value
	}
	loggedIn, redirectURL := media.IsLoggedIn(userID, dynamoClient)
	if loggedIn {
		http.Redirect(w, r, "/managePlaylist", 302)
		return
	}

	t, _ := template.ParseFiles("web/index.html")
	t.Execute(w, struct{ URL string }{redirectURL})
}

func setupCredentials() {
	var maxTracks int64
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
			case "Max_Tracks":
				maxTracks, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 0, 0)
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
	if maxTracks == 0 {
		maxTracks = 5
	}
	media.Setup(clientID, clientSecret, ip, port, int(maxTracks))
}

func makePlaylistHandler(w http.ResponseWriter, r *http.Request) {
	userIDCookie, cookieErr := r.Cookie("user_id")
	if cookieErr != nil {
		log.Printf("Failed to get user id cookie: %v", cookieErr)
		fmt.Fprintf(w, "failed to get user") //really redirect to login here
	}
	log.Printf("Got userID from cookie: %v", userIDCookie.Value)

	//get the form data
	deleteExistingStr := r.FormValue("deleteExisting")
	deleteExisting := deleteExistingStr == "on"

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
	msg := media.MakePlaylist(dynamoClient, userIDCookie.Value, startDate, endDate, deleteExisting, city)
	fmt.Fprintf(w, msg)
}

func setupDB() {
	config := &aws.Config{Region: &region}
	sess := session.Must(session.NewSession(config))
	dynamoClient = dynamodb.New(sess)
}

func getPreScrapeOptions() preScrapeOptions {
	var pdx bool
	var days, eventHours, artistHours, timeout int64
	f, err := os.Open(configFilePath)
	if err == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			line := s.Text()
			parts := strings.Split(line, "=")
			if len(parts) == 2 {
				switch strings.TrimSpace(parts[0]) {
				case "pdxOnly":
					pdx, _ = strconv.ParseBool(strings.TrimSpace(parts[1]))
				case "daysToPreScrape":
					days, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 0, 0)
				case "hoursToCacheEvents":
					eventHours, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 0, 0)
				case "hoursToCacheArtists":
					artistHours, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 0, 0)
				case "timeoutMinutes":
					timeout, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 0, 0)
				}
			}
		}
	}
	//set defaults in case they weren't included or errored parsing
	if days == 0 {
		days = 14
	}
	if eventHours == 0 {
		eventHours = 24
	}
	if artistHours == 0 {
		artistHours = 48
	}
	if timeout == 0 {
		timeout = 30
	}
	ret := preScrapeOptions{eventCacheHours: int(eventHours), artistCacheHours: int(artistHours), pdxOnly: pdx, daysOut: int(days), timeoutMinutes: int(timeout)}
	return ret
}
