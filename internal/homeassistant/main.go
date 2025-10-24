package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/090809/homeassistant-domru/internal/domru/constants"
)

type HAConfig struct {
	Result string `json:"result"`
	Data   struct {
		Interfaces []struct {
			Ipv4 struct {
				Address []string `json:"address"`
			} `json:"ipv4"`
		} `json:"interfaces"`
	} `json:"data"`
}

func GetHomeAssistantNetworkAddressWithPort() (string, error) {
	host, err := GetHomeAssistantNetworkAddress()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:8080", host), nil
}

func GetHomeAssistantNetworkAddress() (string, error) {
	var (
		body             []byte
		err              error
		client           = &http.Client{}
		supervisor_token string
	)

	val, ok := os.LookupEnv("SUPERVISOR_TOKEN")
	if !ok {
		log.Println("SUPERVISOR_TOKEN not set, addon is likely not running in a Home Assistant production environment. This is okay for local development.")
		// Fallback for local development or when not in HA environment.
		// You might want to make "" configurable.
		return "", nil
	}
	supervisor_token = val
	log.Printf("supervisor_token found, attempting to get network address from supervisor.")

	url := constants.API_HA_NETWORK

	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	request = request.WithContext(ctx)

	request.Header = http.Header{
		"Content-Type":  []string{"application/json; charset=UTF-8"},
		"Authorization": []string{"Bearer " + supervisor_token},
	}

	resp, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("supervisor ip request %s", err.Error())
	}

	defer func() {
		err2 := resp.Body.Close()
		if err2 != nil {
			log.Println(err2)
		}
	}()

	if body, err = io.ReadAll(resp.Body); err != nil {
		return "", fmt.Errorf("supervisor ip ReadAll %s", err.Error())
	}

	var haconfig HAConfig

	log.Printf("supervisor ip response: %s", string(body))

	if err := json.Unmarshal(body, &haconfig); err != nil {
		return "", fmt.Errorf("supervisor ip Unmarshal %s", err.Error())
	}

	if haconfig.Result == "ok" && len(haconfig.Data.Interfaces) > 0 {
		address := strings.Split(haconfig.Data.Interfaces[0].Ipv4.Address[0], "/")
		return address[0], nil
	}

	return "", fmt.Errorf("supervisor ip not found")
}
