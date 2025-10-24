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
		m.haHost = "https://home.pallam.dev/"
		mqttHost = "addon_core_mosquitto"
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

	opts.SetWill("domru_proxy/status", "offline", 1, true)

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

	aToken := client.Publish("domru_proxy/status", 1, true, "online")
	aToken.Wait()
	if aToken.Error() != nil {
		m.logger.Error("Failed to publish online status", "error", aToken.Error())
	} else {
		m.logger.Info("Published online status to bridge availability topic")
	}

	// Subscribe to command topics
	commandTopic := "domru/+/command"
	commandToken := m.client.Subscribe(commandTopic, 1, m.commandHandler)
	commandToken.Wait()
	if commandToken.Error() != nil {
		m.logger.Error("Failed to subscribe to command topic", "error", commandToken.Error())
	} else {
		m.logger.Info("Subscribed to command topic", "topic", commandTopic)
	}

	stateTopic := "domru/+/state"
	stateToken := m.client.Subscribe(stateTopic, 1, m.stateHandler)
	stateToken.Wait()
	if stateToken.Error() != nil {
		m.logger.Error("Failed to subscribe to state topic", "error", stateToken.Error())
	} else {
		m.logger.Info("Subscribed to state topic", "topic", stateTopic)
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
			m.publishDoorLock(ac, data.Place.ID)
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

// MqttLock represents the discovery payload for a lock entity.
type MqttLock struct {
	Name              string     `json:"name"`
	UniqueID          string     `json:"unique_id"`
	CommandTopic      string     `json:"command_topic"`
	StateTopic        string     `json:"state_topic"`
	PayloadUnlock     string     `json:"payload_unlock"`
	PayloadLock       string     `json:"payload_lock"`
	StateUnlocked     string     `json:"state_unlocked"`
	StateLocked       string     `json:"state_locked"`
	Optimistic        bool       `json:"optimistic"`
	Device            MqttDevice `json:"device"`
	Icon              string     `json:"icon,omitempty"`
	EntityPicture     string     `json:"entity_picture,omitempty"`
	AvailabilityTopic string     `json:"availability_topic"`
}

func (m *MqttIntegration) publishDoorLock(ac models.AccessControl, placeID int) {
	deviceID := fmt.Sprintf("domru-door_%d_%d", ac.ID, placeID)
	entityID := fmt.Sprintf("%s-open", deviceID)
	discoveryTopic := fmt.Sprintf("homeassistant/lock/%s/config", entityID)
	commandTopic := fmt.Sprintf("domru/%s/command", entityID)
	stateTopic := fmt.Sprintf("domru/%s/state", entityID)

	payload := MqttLock{
		Name:          fmt.Sprintf("Open %s", ac.Name),
		UniqueID:      entityID,
		CommandTopic:  commandTopic,
		StateTopic:    stateTopic,
		PayloadUnlock: "UNLOCK",
		PayloadLock:   "LOCK",
		StateUnlocked: "UNLOCKED",
		StateLocked:   "LOCKED",
		Optimistic:    true,
		Device: MqttDevice{
			Identifiers:  []string{deviceID},
			Name:         ac.Name,
			Model:        "Doorphone",
			Manufacturer: "Dom.ru",
		},
		Icon:              "mdi:door",
		AvailabilityTopic: "domru_proxy/status",
	}

	if m.haHost != "" {
		snapshotURL := constants.GetSnapshotUrl(m.haHost, placeID, ac.ID)
		payload.EntityPicture = snapshotURL
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		m.logger.Error("Failed to marshal lock discovery payload", "error", err)
		return
	}

	// Publish discovery message
	token := m.client.Publish(discoveryTopic, 1, true, jsonPayload)
	token.WaitTimeout(time.Second)

	if token.Error() != nil {
		m.logger.Error("Failed to publish discovery topic", "error", token.Error())
	} else {
		m.logger.Info("Published discovery topic for door lock", "topic", discoveryTopic)
	}

	// Set initial state to LOCKED
	m.client.Publish(stateTopic, 1, true, "LOCKED")
}

func (m *MqttIntegration) commandHandler(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	command := string(msg.Payload())
	m.logger.Info("Received command", "topic", topic, "command", command)

	var acID, placeID int
	_, err := fmt.Sscanf(topic, "domru/domru-door_%d_%d-open/command", &acID, &placeID)
	if err != nil {
		m.logger.Error("Failed to parse access control ID from topic", "topic", topic, "error", err)
		return
	}

	stateTopic := fmt.Sprintf("domru/domru-door_%d_%d-open/state", acID, placeID)

	switch command {
	case "UNLOCK":
		m.logger.Info("Opening door", "placeID", placeID, "accessControlID", acID)
		if err := m.domruAPI.OpenDoor(placeID, acID); err != nil {
			m.logger.Error("Failed to open door", "error", err)
			// Optionally publish a failure state or log
			return
		}

		// Optimistically set state to UNLOCKED, then back to LOCKED after a delay
		m.client.Publish(stateTopic, 1, true, "UNLOCKED")
		time.AfterFunc(5*time.Second, func() {
			m.client.Publish(stateTopic, 1, true, "LOCKED")
		})
	case "LOCK":
		// The door locks automatically, so we just confirm the state.
		m.client.Publish(stateTopic, 1, true, "LOCKED")
	default:
		m.logger.Warn("Received unknown command", "command", command)
	}
}

func (m *MqttIntegration) stateHandler(_ mqtt.Client, msg mqtt.Message) {

}
