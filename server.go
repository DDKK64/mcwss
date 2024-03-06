package mcwss

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/sandertv/mcwss/protocol"
	"github.com/sandertv/mcwss/protocol/command"
)

// Server is the main entry-point of the mcwss package. It allows interfacing with clients connected to it and
// provides methods to ease the use.
type Server struct {
	config   Config
	upgrader websocket.Upgrader

	// multiplayer is buggy, disable it for now.
	//players           map[*Player]bool
	connectionFunc    func(conn *Player)
	disconnectionFunc func(conn *Player)

	salt       []byte
	privateKey *ecdsa.PrivateKey
}

// NewServer initialises and returns a new server. If the configuration passed in was non-nil, that
// configuration is used. If nil is passed, the default configuration is used. (see config.go/defaultConfig())
func NewServer(config *Config) *Server {
	privateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		// Should never happen.
		panic(err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		// Should never happen.
		panic(err)
	}
	server := &Server{
		config:            defaultConfig(),
		connectionFunc:    func(conn *Player) {},
		disconnectionFunc: func(conn *Player) {},
		upgrader: websocket.Upgrader{
			EnableCompression: true,
			// This protocol seems depracated
			//Subprotocols:      []string{MinecraftWSEncryptSubprotocol},
		},
		privateKey: privateKey,
		salt:       salt,
	}
	if config != nil {
		server.config = *config
	}
	return server
}

// Run runs the server and blocks the current goroutine until the server is stopped.
func (server *Server) Run() error {
	http.HandleFunc(server.config.HandlerPattern, server.handleResponse)
	if err := http.ListenAndServe(server.config.Address, nil); err != nil {
		return err
	}
	return nil
}

// OnConnection sets a handler to handle new connections from players. This method must be used to interact
// with players connected.
func (server *Server) OnConnection(handler func(player *Player)) {
	server.connectionFunc = handler
}

// OnDisconnection sets a handler to handle disconnections from players. Note that when setting the handler,
// sending packets to the player will not arrive.
func (server *Server) OnDisconnection(handler func(player *Player)) {
	server.disconnectionFunc = handler
}

// handleResponse handles the websocket response of a client connecting to the server. It first initialises
// the websocket connection, after which it starts processing and sending packets.
func (server *Server) handleResponse(writer http.ResponseWriter, request *http.Request) {
	log.Println("Connection received")
	ws, err := server.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		log.Printf("error upgrading request: %v", err)
		return
	}

	// Initialise the player and add it to the players map.
	player := NewPlayer(ws)

	go func() {
		defer func() {
			// Unsubscribe from all events. The client keeps sending events to the websocket server, even after
			// reconnecting. The client needs to either close the game, or we need to unsubscribe it from all
			// events in order to stop receiving them the next session.
			player.UnsubscribeFromAll()

			server.disconnectionFunc(player)
			player.connected = false

			if err := ws.Close(); err != nil {
				log.Panicf("error closing websocket connection: %v", err)
			}
			player.close <- true
			log.Printf("player %s disconnected", player.name)
		}()
		for {
			msgType, payload, err := ws.ReadMessage()
			if err != nil {
				log.Printf("error reading message from connection: %v", err)
				break
			}
			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
				log.Printf("unexpected message type %v", msgType)
				break
			}

			packet := &protocol.Packet{}
			if err := json.Unmarshal(payload, packet); err != nil {
				log.Printf("malformed JSON packet: %v", err)
				break
			}

			if err := player.handleIncomingPacket(packet); err != nil {
				log.Printf("%v (payload: %v)", err, string(payload))
				continue
			}
		}
	}()

	player.ExecWait(command.LocalPlayerNameRequest(), func(response *command.LocalPlayerName) {
		player.name = response.LocalPlayerName
	})

	if player.name == "" {
		log.Panic("No player name")
	}
	server.connectionFunc(player)
}
