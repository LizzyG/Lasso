package main

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

// var cityMap = map[string]func(time.Time, int, chan eventsRecord, chan error){
// 	"Portland":      scrapeMerc,
// 	"San Francisco": scrapeSF}

var cityMap = map[string]scraper{
	"Portland":      mercScraper{"Music"},
	"San Francisco": sfScraper{"Music"}}

type scraper interface {
	scrapeDate(time.Time, int, chan *eventsRecord, chan error, *eventsRecord)
	getBlankEventRecord() *eventsRecord
}

type mercScraper struct {
	eventType string
}

func (s mercScraper) getBlankEventRecord() *eventsRecord {
	return &eventsRecord{City: "Portland", EventSource: "PortlandMercury", EventType: s.eventType}
}

type sfScraper struct {
	eventType string
}

func (s sfScraper) getBlankEventRecord() *eventsRecord {
	return &eventsRecord{City: "San Francisco", EventSource: "SFWeekly", EventType: s.eventType}
}

func getSupportedCities() []string {
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

type eventsRecord struct {
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

func scrapeDates(start time.Time, end time.Time, city string) ([]string, error) {
	//Mon Jan 2 15:04:05 MST 2006
	log.Printf("scrape start: %v", start)
	log.Printf("scrape end: %v", end)
	log.Printf("city: %v", city)
	//allArtists := make([]string, 0)
	var allArtists []string

	today := getToday()
	if start.Before(today) {
		start = today
	}
	if end.Before(start) {
		end = start
	}
	//startStr := start.Format("2006-01-02")
	//endStr := end.Format("2006-01-02")

	midDate := start
	resCh := make(chan *eventsRecord)
	errCh := make(chan error)
	diffHours := end.Sub(start).Hours()
	diff := (int)(diffHours / 24)
	//diff := int((end.Sub(start).Hours()) / 24)
	log.Printf("diff: %v", diff)
	for i := 0; i <= diff; i++ {
		if i%2 == 0 { //sleep sometimes so the website doesn't throttle
			wait := 2 * i
			time.Sleep(time.Duration(wait) * time.Second)
		}
		log.Printf("going to scrape date %v", midDate)
		//go cityMap[city](midDate.Format("2006-01-02"), 1, resCh, errCh)
		//go cityMap[city](midDate, 1, resCh, errCh)
		//////////////
		cityScraper := cityMap[city]
		e := cityScraper.getBlankEventRecord()
		e.Date = midDate
		getEventFromDb(e)
		timeDiff := time.Now().Sub(e.ScrapeDate)
		dur := time.Hour * 12
		log.Printf("time comparison: %v", timeDiff < dur)
		if e.FullListing && len(e.Artists) > 0 && (time.Now().Sub(e.ScrapeDate) < time.Hour*12) {
			log.Println("using events from db")
			e.fromDb = true
			go func() { resCh <- e }()
		} else {
			////////////
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
			log.Println("scrapeDates got one channel response")
			//a.Artists = sliceUniqMap(a.Artists)
			allArtists = append(allArtists, a.Artists...)
			if !a.fromDb {
				go addEventToDb(a)
			}
			cnt++
			done = cnt > diff
		case e = <-errCh:
			log.Printf("got an error from the go routine: %v", e)
			//done = true
		case <-time.After(120 * time.Second):
			log.Println("timed out watiting for response from scraper")
			e = errors.New("Timed out while processing your request")
			done = true
		}
	}
	allArtists = sliceUniqMap(allArtists)
	return allArtists, e
}

//func scrapeMerc(dateStr string, page int, resCh chan []string, errCh chan error) {
func (s mercScraper) scrapeDate(date time.Time, page int, resCh chan *eventsRecord, errCh chan error, event *eventsRecord) {
	dateStr := date.Format("2006-01-02")

	artists := make([]string, 0)
	if page <= 1 {
		page = 1
	}

	site := "https://www.portlandmercury.com/events/music/" + dateStr

	c := colly.NewCollector()
	//Googlebot/2.1
	//c.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/73.0.3683.86 Safari/537.36"
	//c.OnHTML("h3.calendar-post-title>a", func(e *colly.HTMLElement) {
	c.OnHTML("h3.event-row-title>a", func(e *colly.HTMLElement) {
		text := strings.Trim(e.Text, "\n ")
		artists = append(artists, strings.Split(text, ",")...)
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
				//resCh <- &eventsRecord{City: "Portland", EventSource: "PortlandMercury", Date: date, Artists: artists, ScrapeDate: time.Now(), FullListing: false, EventType: "Music"}
				event.Artists = append(event.Artists, artists...)
				event.ScrapeDate = time.Now()
				event.FullListing = false
				resCh <- event
			}
		} else {
			//there's not another page, return
			//this probably isn't quite right, check if there can be a race condition
			log.Printf("end of listings for date %v", dateStr)
			//resCh <- &eventsRecord{City: "Portland", EventSource: "PortlandMercury", Date: date, Artists: artists, ScrapeDate: time.Now(), FullListing: true, EventType: "Music"}
			event.Artists = append(event.Artists, artists...)
			event.ScrapeDate = time.Now()
			event.FullListing = true
			resCh <- event
		}

	})

	err := c.Visit(site)
	if err != nil {
		log.Printf("error visiting the site: %v", err)
		//resCh <- &eventsRecord{City: "Portland", EventSource: "PortlandMercury", Date: date, Artists: artists, ScrapeDate: time.Now(), FullListing: false, EventType: "Music"}
		event.Artists = append(event.Artists, artists...)
		event.ScrapeDate = time.Now()
		event.FullListing = false
		resCh <- event
		errCh <- err
	}
}

func (s sfScraper) scrapeDate(date time.Time, page int, ch chan *eventsRecord, errCh chan error, e *eventsRecord) {
	dateStr := date.Format("2006-01-02")
	//errCh <- errors.New("not implemented")
	artists := make([]string, 0)
	site := "https://archives.sfweekly.com/sanfrancisco/EventSearch?eventSection=2205482&date=" + dateStr //2019-04-05"
	c := colly.NewCollector()

	//c.OnHTML("h3.calendar-post-title>a", func(e *colly.HTMLElement) {
	//>thead>tr>td>a
	c.OnHTML("#ConcertResults>table>thead>tr>td>a", func(e *colly.HTMLElement) {
		attr := e.Attr("href")
		if strings.Contains(attr, "Event") {

			text := strings.Trim(e.Text, "\n ")
			//fmt.Println(text)
			if text != "" {
				artists = append(artists, text)
			}
			//need to also follow the event link to get openers
		}
	})
	c.OnHTML("#gridFooter", func(e *colly.HTMLElement) {
		ch <- &eventsRecord{City: "San Francisco", EventSource: "SFWeekly", Date: date, Artists: artists, ScrapeDate: time.Now(), FullListing: true, EventType: "Music"}
	})
	//need to send results to channel

	err := c.Visit(site)
	if err != nil {
		fmt.Printf("error: %v", err)
		ch <- &eventsRecord{City: "San Francisco", EventSource: "SFWeekly", Date: date, Artists: artists, ScrapeDate: time.Now(), FullListing: false, EventType: "Music"}
		errCh <- err
	}
}

func scrapeSfStation(dateStr string) {
	site := "https://www.sfstation.com/music/calendar/" + dateStr //04-05-2019
	//https://www.sfstation.com/music/calendar/2/04-05-2019  optional page before date
	c := colly.NewCollector()

	//c.OnHTML("h3.calendar-post-title>a", func(e *colly.HTMLElement) {
	//>thead>tr>td>a
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

//func addEventToDb(date time.Time, city string, source string, eventType string, artists []string) {
func addEventToDb(e *eventsRecord) {
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

func getEventKeyStr(e *eventsRecord) string {
	return e.City + "_" + e.EventSource + "_" + e.EventType
}

func getEventFromDb(e *eventsRecord) error {
	dateStr := e.Date.Format("2006-01-02")
	//can't do city validation here because of init loop with cityMap
	// if _, ok := cityMap[e.City]; !ok {
	// 	log.Println("Invalid city, will not query db")
	// }
	log.Println("querying db for events")
	KeyStr := getEventKeyStr(e)
	get := dynamodb.GetItemInput{TableName: aws.String("Events"),
		Key: map[string]*dynamodb.AttributeValue{"DateStr": {S: aws.String(dateStr)}, "KeyStr": {S: aws.String(KeyStr)}}}
	res, err := dynamoClient.GetItem(&get)
	log.Printf("retrieved events for date %v: %v", dateStr, res.Item["Artists"])
	//e2 := eventsRecord{}
	err = dynamodbattribute.UnmarshalMap(res.Item, &e)
	//load res back into e
	//e.Artists = res.Item["Artists"].
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
