package api

import (
	"errors"

	"github.com/mhmdxsadk/snatcher/internal/download"
)

type downloadRequest struct {
	URL         string `json:"url"`
	Quality     string `json:"quality"`
	Mode        string `json:"mode"`
	AudioFormat string `json:"audioFormat"`
}

// Empty options use Quick Snatch defaults. Validate even irrelevant options so
// misspelled preferences never silently change the requested download.
func (r downloadRequest) options() (download.Request, error) {
	options := download.Request{Quality: r.Quality, Mode: r.Mode, AudioFormat: r.AudioFormat}
	if options.Quality == "" {
		options.Quality = "1080"
	}
	if options.Mode == "" {
		options.Mode = "auto"
	}
	if options.AudioFormat == "" {
		options.AudioFormat = "mp3"
	}
	switch options.Quality {
	case "720", "1080", "1440", "2160", "max":
	default:
		return download.Request{}, errors.New("quality must be one of: 720, 1080, 1440, 2160, max.")
	}
	switch options.Mode {
	case "auto", "audio", "mute":
	default:
		return download.Request{}, errors.New("mode must be one of: auto, audio, mute.")
	}
	switch options.AudioFormat {
	case "mp3", "best":
	default:
		return download.Request{}, errors.New("audioFormat must be one of: mp3, best.")
	}
	return options, nil
}
