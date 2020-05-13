package media

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"google.golang.org/api/googleapi/transport"
	"google.golang.org/api/youtube/v3"
)

//remove this, just being lazy to get started
var apiKey = "AIzaSyDeQ2PyoACu1TAWzo_DN-hpaTL502RmFXo"

func SearchManyArtists(artists []string) []string {

	ch := make(chan []string, len(artists)+1)
	var wg sync.WaitGroup
	for _, artist := range artists {
		wg.Add(1)
		go searchSingleArtist(artist, ch, &wg)
	}
	wg.Wait()
	close(ch)
	var videoIds []string
	for vids := range ch {
		videoIds = append(videoIds, vids...)
	}

	return videoIds
}

func searchSingleArtist(artist string, ch chan<- []string, wg *sync.WaitGroup) {
	defer wg.Done()
	client := &http.Client{
		Transport: &transport.APIKey{Key: apiKey},
	}

	service, err := youtube.New(client)
	if err != nil {
		log.Fatalf("Error creating new YouTube client: %v", err)
	}

	// Make the API call to YouTube.
	query := "\"" + artist + "\""
	call := service.Search.List("id,snippet").
		Q(query).Type("video").
		MaxResults(5)
	response, err := call.Do()
	if err != nil {
		log.Printf("youtube query failed: %v", err)
	}
	fmt.Println(response)

	// Group video, channel, and playlist results in separate lists.
	videos := make(map[string]string)
	videoIds := make([]string, len(response.Items))

	// Iterate through each item and add it to the correct list.
	for i, item := range response.Items {
		switch item.Id.Kind {
		case "youtube#video":
			videos[item.Id.VideoId] = item.Snippet.Title
			videoIds[i] = item.Id.VideoId
		}
	}

	fmt.Println("Videos", videos)
	ch <- videoIds
}

//<iframe width="560" height="315" src="https://www.youtube.com/embed/knrJTPhmMKY" frameborder="0" allow="accelerometer; autoplay; encrypted-media; gyroscope; picture-in-picture" allowfullscreen></iframe>
