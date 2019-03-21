package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gocolly/colly"
)

func getToday() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

func scrapeDates(start time.Time, end time.Time) []string {
	//Mon Jan 2 15:04:05 MST 2006

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
	ch := make(chan []string)
	diff := int((end.Sub(start).Hours()) / 24)
	for i := 0; i <= diff; i++ {
		go scrape(midDate.Format("2006-01-02"), 1, ch)
		midDate = midDate.Add(time.Hour * 24)
	}
	log.Printf("diff: %v", diff)
	for i := 0; i <= diff; i++ {
		log.Printf("waiting for channel response %v", i)
		a := <-ch
		log.Println("scrapeDates got one channel response")
		allArtists = append(allArtists, a...)
	}
	close(ch)
	return allArtists
}

func scrape(dateStr string, page int, ch chan []string) {
	artists := make([]string, 0)
	if page <= 1 {
		page = 1
	}

	site := "https://www.portlandmercury.com/events/music/" + dateStr

	c := colly.NewCollector()

	//c.OnHTML("h3.calendar-post-title>a", func(e *colly.HTMLElement) {
	c.OnHTML("h3.event-row-title>a", func(e *colly.HTMLElement) {
		log.Println("found a calendar listing")
		text := strings.Trim(e.Text, "\n ")
		artists = append(artists, strings.Split(text, ",")...)
	})
	c.OnHTML("li.next>a", func(e *colly.HTMLElement) {
		if strings.Contains(e.Text, "Next on") {
			log.Printf("there's another page for date %v", dateStr)
			page = page + 1
			site = fmt.Sprintf(site+"?page=%v&view_id=events", page)
			log.Println(site)
			c.Visit(site)
		} else {
			//there's not another page, return
			//this probably isn't quite right, check if there can be a race condition
			log.Printf("end of listings for date %v", dateStr)
			ch <- artists
		}

	})

	err := c.Visit(site)
	if err != nil {
		log.Printf("error visiting the site: %v", err)
	}
}
