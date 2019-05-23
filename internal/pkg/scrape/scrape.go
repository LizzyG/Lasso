package scrape

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"

	"github.com/gocolly/colly"
)

var cityMap = map[string]scraper{
	"Portland":      mercScraper{"Music"},
	"San Francisco": sfScraper{"Music"}}

type scraper interface {
	scrapeDate(time.Time, int, chan *EventsRecord, chan error, *EventsRecord)
	getBlankEventRecord() *EventsRecord
	getURL() string
}

type mercScraper struct {
	eventType string
}

func (s mercScraper) getBlankEventRecord() *EventsRecord {
	return &EventsRecord{City: "Portland", EventSource: "PortlandMercury", EventType: s.eventType}
}

func (s mercScraper) getURL() string {
	return "https://www.portlandmercury.com/events/" + s.eventType
}

type sfScraper struct {
	eventType string
}

func (s sfScraper) getBlankEventRecord() *EventsRecord {
	return &EventsRecord{City: "San Francisco", EventSource: "SFWeekly", EventType: s.eventType}
}

func (s sfScraper) getURL() string {
	return "https://archives.sfweekly.com/sanfrancisco/EventSearch?eventSection=2205482"
}

func GetSupportedCities() []string {
	cities := make([]string, len(cityMap))
	i := 0
	for KeyStr := range cityMap {
		cities[i] = KeyStr
		i++
	}
	return cities
}

func getToday() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

type EventsRecord struct {
	KeyStr      string
	City        string
	EventSource string
	EventType   string
	Date        time.Time `dynamodbav:"-"`
	DateStr     string
	Artists     []string
	ScrapeDate  time.Time
	FullListing bool
	fromDb      bool `dynamodbav:"-"`
}

func ScrapeDates(dynamoClient *dynamodb.DynamoDB, start time.Time, end time.Time, city string) ([]string, string, error) {
	//Mon Jan 2 15:04:05 MST 2006
	log.Printf("scrape start: %v", start)
	log.Printf("scrape end: %v", end)
	log.Printf("city: %v", city)
	var allArtists []string

	today := getToday()
	if start.Before(today) {
		start = today
	}
	if end.Before(start) {
		end = start
	}

	midDate := start
	resCh := make(chan *EventsRecord)
	errCh := make(chan error)
	diff := int((end.Sub(start).Hours()) / 24)
	log.Printf("diff: %v", diff)
	cityScraper := cityMap[city]
	for i := 0; i <= diff; i++ {
		if i%2 == 0 { //sleep sometimes so the website doesn't throttle
			wait := 2 * i
			time.Sleep(time.Duration(wait) * time.Second)
		}
		//log.Printf("going to scrape date %v", midDate)

		e := cityScraper.getBlankEventRecord()
		e.Date = midDate
		getEventFromDb(dynamoClient, e)
		timeDiff := time.Now().Sub(e.ScrapeDate)
		dur := time.Hour * 12
		log.Printf("time comparison: %v", timeDiff < dur)
		if e.FullListing && len(e.Artists) > 0 && (time.Now().Sub(e.ScrapeDate) < time.Hour*12) {
			log.Println("using events from db")
			e.fromDb = true
			go func() { resCh <- e }()
		} else {
			log.Println("scraping events from db")
			go cityScraper.scrapeDate(midDate, 1, resCh, errCh, e)
		}
		midDate = midDate.Add(time.Hour * 24)
	}

	done := false
	cnt := 0
	var e error
	for !done {
		select {
		case a := <-resCh:
			//log.Println("scrapeDates got one channel response")
			allArtists = append(allArtists, a.Artists...)
			if !a.fromDb {
				go addEventToDb(dynamoClient, a)
			}
			cnt++
			done = cnt > diff
		case e = <-errCh:
			log.Printf("got an error from the go routine: %v", e)
		case <-time.After(120 * time.Second):
			log.Println("timed out watiting for response from scraper")
			e = errors.New("Timed out while processing your request")
			done = true
		}
	}
	allArtists = sliceUniqMap(allArtists)
	url := cityScraper.getURL()
	return allArtists, url, e
}

func (s mercScraper) scrapeDate(date time.Time, page int, resCh chan *EventsRecord, errCh chan error, event *EventsRecord) {
	dateStr := date.Format("2006-01-02")
	artists := make([]string, 0)
	if page <= 1 {
		page = 1
	}

	site := "https://www.portlandmercury.com/events/music/" + dateStr

	c := colly.NewCollector()
	c.SetRequestTimeout(time.Second * 10)
	c.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/74.0.3729.131 Safari/537.36"
	c.OnHTML("h3.event-row-title>a", func(e *colly.HTMLElement) {
		text := strings.Trim(e.Text, "\n ")
		newArtists := strings.Split(text, ",")
		for i, a := range newArtists {
			newArtists[i] = strings.TrimSpace(a)
		}
		artists = append(artists, newArtists...)
	})
	c.OnHTML("li.next>a", func(e *colly.HTMLElement) {
		if strings.Contains(e.Text, "Next on") {
			log.Printf("there's another page for date %v", dateStr)
			page = page + 1
			site = fmt.Sprintf(site+"?page=%v&view_id=events", page)
			log.Println(site)
			err := c.Visit(site)
			if err != nil {
				log.Printf("error visiting the site: %v", err)
				event.Artists = append(event.Artists, artists...)
				event.ScrapeDate = time.Now()
				event.FullListing = false
				resCh <- event
			}
		} else {
			//there's not another page, return
			//this probably isn't quite right, check if there can be a race condition
			log.Printf("end of listings for date %v", dateStr)
			//clear out any old artists that may have been in the event because we got the full thing with no errors
			event.Artists = nil
			event.Artists = append(event.Artists, artists...)
			event.ScrapeDate = time.Now()
			event.FullListing = true
			resCh <- event
		}

	})

	err := c.Visit(site)
	if err != nil {
		log.Printf("error visiting the site: %v", err)
		event.Artists = append(event.Artists, artists...)
		event.ScrapeDate = time.Now()
		event.FullListing = false
		resCh <- event
		errCh <- err
	}
}

func (s sfScraper) scrapeDate(date time.Time, page int, ch chan *EventsRecord, errCh chan error, e *EventsRecord) {
	dateStr := date.Format("2006-01-02")
	artists := make([]string, 0)
	site := "https://archives.sfweekly.com/sanfrancisco/EventSearch?eventSection=2205482&date=" + dateStr //2019-04-05"
	c := colly.NewCollector()
	c.SetRequestTimeout(time.Second * 20)

	c.OnHTML("#ConcertResults>table>thead>tr>td>a", func(e *colly.HTMLElement) {
		attr := e.Attr("href")
		if strings.Contains(attr, "Event") {
			text := strings.Trim(e.Text, "\n ")
			if text != "" {
				artists = append(artists, text)
			}
			//need to also follow the event link to get openers
		}
	})
	c.OnHTML("#gridFooter", func(e *colly.HTMLElement) {
		ch <- &EventsRecord{City: "San Francisco", EventSource: "SFWeekly", Date: date, Artists: artists, ScrapeDate: time.Now(), FullListing: true, EventType: "Music"}
	})

	err := c.Visit(site)
	if err != nil {
		fmt.Printf("error: %v", err)
		ch <- &EventsRecord{City: "San Francisco", EventSource: "SFWeekly", Date: date, Artists: artists, ScrapeDate: time.Now(), FullListing: false, EventType: "Music"}
		errCh <- err
	}
}

func scrapeSfStation(dateStr string) {
	site := "https://www.sfstation.com/music/calendar/" + dateStr //04-05-2019
	//https://www.sfstation.com/music/calendar/2/04-05-2019  optional page before date
	c := colly.NewCollector()
	var artists []string
	c.OnHTML("#list>tbody>tr>td>div>a", func(e *colly.HTMLElement) {
		attr := e.Attr("href")
		if strings.Contains(attr, "Event") {
			text := strings.Trim(e.Text, "\n ")
			fmt.Println(text)
			artists = append(artists, text)
		}
	})

	err := c.Visit(site)
	if err != nil {
		fmt.Printf("error: %v", err)
	}
}

func addEventToDb(dynamoClient *dynamodb.DynamoDB, e *EventsRecord) {
	if len(e.Artists) == 0 {
		return
	}
	dateStr := e.Date.Format("2006-01-02")
	//validate KeyStr fields
	if _, ok := cityMap[e.City]; !ok {
		log.Println("Invalid city, will not insert into db")
	}
	log.Println("adding event to db")
	//remove potential duplicates
	KeyStr := getEventKeyStr(e)
	e.ScrapeDate = e.ScrapeDate.Round(time.Minute)
	//for now just do a full on replace, we may want to add artists to the set versus a straight replace though
	updateExp := "set City=:City, FullListing=:FullListing, EventSource=:EventSource, EventType=:EventType, Artists=:Artists, ScrapeDate=:ScrapeDate"
	val, err := dynamodbattribute.MarshalMap(e)
	if err != nil {
		fmt.Println("error marshalling ", err)
	}
	modVal := make(map[string]*dynamodb.AttributeValue, len(val)-2)
	for k, v := range val {
		if k != "KeyStr" && k != "DateStr" {
			modKey := ":" + k
			modVal[modKey] = v
		}
	}
	put := dynamodb.UpdateItemInput{TableName: aws.String("Events"),
		Key: map[string]*dynamodb.AttributeValue{"DateStr": {S: aws.String(dateStr)},
			"KeyStr": {S: aws.String(KeyStr)}},
		UpdateExpression:          aws.String(updateExp),
		ExpressionAttributeValues: modVal}
	_, err = dynamoClient.UpdateItem(&put)
	if err != nil {
		log.Printf("Error adding event to db: %v", err)
	}
}

func getEventKeyStr(e *EventsRecord) string {
	return e.City + "_" + e.EventSource + "_" + e.EventType
}

func getEventFromDb(dynamoClient *dynamodb.DynamoDB, e *EventsRecord) error {
	dateStr := e.Date.Format("2006-01-02")
	//can't do city validation here because of init loop with cityMap
	// if _, ok := cityMap[e.City]; !ok {
	// 	log.Println("Invalid city, will not query db")
	// }
	//log.Println("querying db for events")
	KeyStr := getEventKeyStr(e)
	get := dynamodb.GetItemInput{TableName: aws.String("Events"),
		Key: map[string]*dynamodb.AttributeValue{"DateStr": {S: aws.String(dateStr)}, "KeyStr": {S: aws.String(KeyStr)}}}
	res, err := dynamoClient.GetItem(&get)
	//log.Printf("retrieved events for date %v: %v", dateStr, res.Item["Artists"])
	err = dynamodbattribute.UnmarshalMap(res.Item, &e)
	return err
}

func sliceUniqMap(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	j := 0
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		s[j] = v
		j++
	}
	return s[:j]
}

//check db for missing/expiring dates for the relevant cities
func PreScrape(dynamoClient *dynamodb.DynamoDB, daysOut int, cacheHours int, timeoutMinutes int, pdxOnly bool) []string {
	resCh := make(chan *EventsRecord)
	errCh := make(chan error)
	startDate := time.Now()
	var cities []string
	if pdxOnly {
		cities = []string{"Portland"}
	} else {
		cities = GetSupportedCities()
	}
	var allArtists []string
	total := 0
	for i := 0; i <= daysOut; i++ {
		for _, city := range cities {
			scraper := cityMap[city]
			e := scraper.getBlankEventRecord()
			e.Date = startDate.AddDate(0, 0, i)
			//first check the db
			getEventFromDb(dynamoClient, e)
			dur := time.Duration(cacheHours) * time.Hour
			if len(e.Artists) == 0 || (time.Now().Sub(e.ScrapeDate) > dur || !e.FullListing) {
				go scraper.scrapeDate(startDate.AddDate(0, 0, i), 1, resCh, errCh, e)
				total = total + 1
			} else {
				allArtists = append(allArtists, e.Artists...)
			}
		}
	}

	cnt := 0
	done := total == cnt //in case there were no go routines launched

	timeout := time.Duration(timeoutMinutes) * time.Minute
	for !done {
		select {
		case event := <-resCh:
			cnt++
			allArtists = append(allArtists, event.Artists...)
			addEventToDb(dynamoClient, event)
			if cnt >= total {
				done = true
			}
		case err := <-errCh:
			log.Println("Error pre scraping: ", err)
		case <-time.After(timeout):
			log.Println("Timed out pre scraping")
			done = true
		}
	}
	allArtists = sliceUniqMap(allArtists)
	return allArtists
}
