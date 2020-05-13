package main

import (
	"fmt"
	"lasso/internal/pkg/media"
	"testing"
)

func TestBasic(t *testing.T) {
	artists := []string{"Jackstraw", "Wimps", "Escape From the Zoo"}
	vids := media.SearchManyArtists(artists)
	fmt.Println(vids)
}
