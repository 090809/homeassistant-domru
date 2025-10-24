package homeassistant

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/090809/homeassistant-domru/internal/domru"
	"github.com/090809/homeassistant-domru/internal/domru/constants"
	"github.com/090809/homeassistant-domru/internal/domru/models"
)

const (
	mqttHostEnv     = "MQTT_HOST"
	mqttPortEnv     = "MQTT_PORT"
	mqttUsernameEnv = "MQTT_USER"
	mqttPasswordEnv = "MQTT_PASSWORD"
)

// MqttIntegration handles the connection and communication with Home Assistant via MQTT.
type MqttIntegration struct {
	client   mqtt.Client
	logger   *slog.Logger
	domruAPI *domru.APIWrapper
	haHost   string

	mqttPort     int
	mqttUsername string
	mqttPassword string
}

// NewMqttIntegration creates and configures the MQTT integration.
func NewMqttIntegration(
	domruAPI *domru.APIWrapper,
	logger *slog.Logger,
) *MqttIntegration {
	return &MqttIntegration{
		domruAPI: domruAPI,
		logger:   logger,
	}
}

// Start connects to the MQTT broker and sets up device discovery.
func (m *MqttIntegration) Start() {
	var mqttHost string
	if _, ok := os.LookupEnv("SUPERVISOR_TOKEN"); ok {
		m.haHost = "172.30.32.1"
		mqttHost = "172.30.32.1"
	} else {
		return
	}

	mqttPort := 1883
	mqttUser := "domru_proxy"
	mqttPass := "domru_proxy"

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", mqttHost, mqttPort))
	opts.SetClientID(fmt.Sprintf("domru_proxy_%d", time.Now().Unix()))
	opts.SetUsername(mqttUser)
	opts.SetPassword(mqttPass)
	opts.OnConnect = m.connectHandler
	opts.OnConnectionLost = m.connectionLostHandler

	m.logger.Info("Connecting to MQTT broker...")
	m.client = mqtt.NewClient(opts)
	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		m.logger.Error("Failed to connect to MQTT broker", "error", token.Error())
		return
	}
}

func (m *MqttIntegration) connectHandler(client mqtt.Client) {
	m.logger.Info("Connected to MQTT broker")

	// Subscribe to command topics
	commandTopic := "domru/domru_door_*/command"
	token := m.client.Subscribe(commandTopic, 1, m.commandHandler)
	token.Wait()
	if token.Error() != nil {
		m.logger.Error("Failed to subscribe to command topic", "error", token.Error())
	} else {
		m.logger.Info("Subscribed to command topic", "topic", commandTopic)
	}

	go m.discoverDevices()
}

func (m *MqttIntegration) connectionLostHandler(client mqtt.Client, err error) {
	m.logger.Warn("MQTT connection lost", "error", err)
}

func (m *MqttIntegration) Stop() {
	if m.client != nil && m.client.IsConnected() {
		m.logger.Info("Disconnecting from MQTT broker")
		m.client.Disconnect(250) // 250ms timeout
	}
}

func (m *MqttIntegration) discoverDevices() {
	// Allow some time for the connection to be fully established
	time.Sleep(2 * time.Second)

	placesResponse, err := m.domruAPI.RequestPlaces()
	if err != nil {
		m.logger.Error("Failed to get places for MQTT discovery", "error", err)
		return
	}

	for _, data := range placesResponse.Data {
		m.logger.Info("Discovering doorphone",
			"placeID", data.Place.ID,
			"accessControls (len)", len(data.Place.AccessControls),
			"accessControls", data.Place.AccessControls,
			"cameras", len(data.Place.Cameras),
		)
		for _, ac := range data.Place.AccessControls {
			m.publishDoorButton(ac, data.Place.ID)
		}
	}
}

// MqttDevice represents a Home Assistant device.
type MqttDevice struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Model        string   `json:"model"`
	Manufacturer string   `json:"manufacturer"`
}

// MqttButton represents the discovery payload for a button entity.
type MqttButton struct {
	Name              string     `json:"name"`
	UniqueID          string     `json:"unique_id"`
	CommandTopic      string     `json:"command_topic"`
	Device            MqttDevice `json:"device"`
	Icon              string     `json:"icon,omitempty"`
	EntityPicture     string     `json:"entity_picture,omitempty"`
	AvailabilityTopic string     `json:"availability_topic"`
}

func (m *MqttIntegration) publishDoorButton(ac models.AccessControl, placeID int) {
	deviceID := fmt.Sprintf("domru-door:%d:%d", ac.ID, placeID)
	entityID := fmt.Sprintf("%s-open", deviceID)
	discoveryTopic := fmt.Sprintf("homeassistant/button/%s/config", entityID)
	commandTopic := fmt.Sprintf("domru/%s/command", entityID)
	availabilityTopic := fmt.Sprintf("domru/%s/status", deviceID)

	payload := MqttButton{
		Name:         fmt.Sprintf("Open %s", ac.Name),
		UniqueID:     entityID,
		CommandTopic: commandTopic,
		Device: MqttDevice{
			Identifiers:  []string{deviceID},
			Name:         ac.Name,
			Model:        "Doorphone",
			Manufacturer: "Dom.ru",
		},
		Icon:              "mdi:door",
		AvailabilityTopic: availabilityTopic,
	}

	if m.haHost != "" {
		snapshotURL := constants.GetSnapshotUrl(m.haHost, placeID, ac.ID)
		payload.EntityPicture = snapshotURL
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		m.logger.Error("Failed to marshal button discovery payload", "error", err)
		return
	}

	// Publish discovery message
	token := m.client.Publish(discoveryTopic, 1, true, jsonPayload)
	token.Wait()
	if token.Error() != nil {
		m.logger.Error("Failed to publish discovery topic", "error", token.Error())
	} else {
		m.logger.Debug("Published discovery topic for doorbutton", "topic", discoveryTopic)
	}
}

func (m *MqttIntegration) commandHandler(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	m.logger.Debug("Received command", "topic", topic)

	var acID, placeID int

	_, err := fmt.Sscanf(topic, "domru/domru-door:%d:%d-open/command", &acID, &placeID)
	if err != nil {
		m.logger.Error("Failed to parse access control ID from topic", "topic", topic, "error", err)
		return
	}

	m.logger.Info("Opening door", "placeID", placeID, "accessControlID", acID)
	if err := m.domruAPI.OpenDoor(placeID, acID); err != nil {
		m.logger.Error("Failed to open door", "error", err)
	}
}
