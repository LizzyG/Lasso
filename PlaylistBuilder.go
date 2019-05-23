package main

import (
	"bufio"
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"lasso/internal/pkg/media"
	"lasso/internal/pkg/scrape"

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
		homeHandler(w, r)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		client := media.CompleteAuth(ctx, w, r)
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

	http.HandleFunc("/preScrape", func(w http.ResponseWriter, r *http.Request) {
		log.Println("got a request for /preScrape")
		daysOut := 14
		cacheHours := 24
		pdxOnly := false
		artists := scrape.PreScrape(dynamoClient, daysOut, cacheHours, 15, pdxOnly)
		media.PreSearch(dynamoClient, artists, 48)

	})
	http.ListenAndServe(":"+port, nil)
}

func manageHandler(w http.ResponseWriter, r *http.Request) {
	if userIDCookie, _ := r.Cookie("user_id"); userIDCookie != nil {

		err := media.ManagePlaylist(userIDCookie.Value)
		if err == nil {
			today := time.Now().Format("2006-01-02")
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
	loggedIn, redirectURL := media.IsLoggedIn(userID)
	if loggedIn {
		http.Redirect(w, r, "/managePlaylist", 302)
		return
	}

	t, _ := template.ParseFiles("web/index.html")
	t.Execute(w, struct{ URL string }{redirectURL})
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

	media.Setup(clientID, clientSecret, ip, port)
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
