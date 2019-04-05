package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gocolly/colly"
)

var cityMap = map[string]func(string, int, chan []string, chan error){
	"Portland":      scrapeMerc,
	"San Francisco": scrapeSF}

func getSupportedCities() []string {
	cities := make([]string, len(cityMap))
	i := 0
	for key := range cityMap {
		cities[i] = key
		i++
	}
	return cities
}

func getToday() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
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
	resCh := make(chan []string)
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
		go cityMap[city](midDate.Format("2006-01-02"), 1, resCh, errCh)
		midDate = midDate.Add(time.Hour * 24)
	}
	// for i := 0; i <= diff; i++ {
	// 	log.Printf("waiting for channel response %v", i)	//add a timeout here so we don't wait forever
	// 	a := <-resCh
	// 	log.Println("scrapeDates got one channel response")
	// 	allArtists = append(allArtists, a...)
	// }

	done := false
	cnt := 0
	var e error
	for !done {
		select {
		case a := <-resCh:
			log.Println("scrapeDates got one channel response")
			allArtists = append(allArtists, a...)
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
	return allArtists, e
}

func scrapeMerc(dateStr string, page int, resCh chan []string, errCh chan error) {
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
				resCh <- artists
			}
		} else {
			//there's not another page, return
			//this probably isn't quite right, check if there can be a race condition
			log.Printf("end of listings for date %v", dateStr)
			resCh <- artists
		}

	})

	err := c.Visit(site)
	if err != nil {
		log.Printf("error visiting the site: %v", err)
		resCh <- artists
		errCh <- err
	}
}

func scrapeSF(dateStr string, page int, ch chan []string, errCh chan error) {
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
		ch <- artists
	})
	//need to send results to channel

	err := c.Visit(site)
	if err != nil {
		fmt.Printf("error: %v", err)
		ch <- artists
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
