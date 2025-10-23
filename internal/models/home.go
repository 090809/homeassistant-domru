package models

import (
	models "github.com/090809/homeassistant-domru/internal/domru/models"
)

type HomePageData struct {
	BaseURL    string
	LoginError string
	Phone      string
	Cameras    models.CamerasResponse
	Places     models.PlacesResponse
}
