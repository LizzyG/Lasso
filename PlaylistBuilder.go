package main

import (
	"bufio"
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"lasso/internal/pkg/media"
	"lasso/internal/pkg/scrape"

	"github.com/recoilme/slowpoke"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/aws/aws-sdk-go/aws"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
)

type dbFile int

const (
	artistsDb dbFile = iota
)

type idType int

const (
	spotifyId idType = iota
)

var ch = make(chan *spotify.Client)
var idTypeNames = map[idType]string{spotifyId: "SpotifyID"}

var dbFiles = map[dbFile]string{artistsDb: "db/artists.db"}

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
	defer closeDb()

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

	http.HandleFunc("/dumpDb", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got dumpDb request")
		dbDump(artistsDb)
		fmt.Fprint(w, "Dumped")
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
	deleteExisting, delErr := strconv.ParseBool(deleteExistingStr)
	if delErr != nil {
		deleteExisting = false
	}
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
