package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type Message struct {
	RoomID   string `json:"room_id"`
	Username string `json:"username"`
	Content  string `json:"content"`
	FileData []byte `json:"file_data"`
	FileName string `json:"file_name"`
	FileType string `json:"file_type"`
	IsFile   bool   `json:"is_file"`
}

type Client struct {
	socket   *websocket.Conn
	username string
	roomID   string
	send     chan Message
	mu       sync.Mutex
}

type Room struct {
	clients map[*Client]bool
	mutex   sync.RWMutex
}

type RoomManager struct {
	rooms map[string]*Room
	mutex sync.RWMutex
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	roomManager = NewRoomManager()
)

func NewRoomManager() *RoomManager {
	return &RoomManager{
		rooms: make(map[string]*Room),
	}
}

func (rm *RoomManager) GetOrCreateRoom(roomID string) *Room {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	if room, exists := rm.rooms[roomID]; exists {
		return room
	}

	newRoom := &Room{
		clients: make(map[*Client]bool),
	}
	rm.rooms[roomID] = newRoom
	return newRoom
}

func (room *Room) BroadcastMessage(message Message) {
	room.mutex.RLock()
	defer room.mutex.RUnlock()

	log.Printf("Broadcasting message in room %s: %+v", message.RoomID, message)

	for client := range room.clients {
		select {
		case client.send <- message:
			log.Printf("Sending message to client in room %s", message.RoomID)
		default:
			log.Printf("Failed to send message to client in room %s", message.RoomID)
			close(client.send)
			delete(room.clients, client)
		}
	}
}

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room")
	username := r.URL.Query().Get("username")

	if roomID == "" || username == "" {
		http.Error(w, "Room ID and Username are required", http.StatusBadRequest)
		return
	}

	socket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	client := &Client{
		socket:   socket,
		username: username,
		roomID:   roomID,
		send:     make(chan Message, 256),
	}

	room := roomManager.GetOrCreateRoom(roomID)
	room.mutex.Lock()
	room.clients[client] = true
	room.mutex.Unlock()

	log.Printf("New client connected to room %s with username %s", roomID, username)

	go client.WritePump()
	go client.ReadPump()
}

func (c *Client) WritePump() {
	defer func() {
		c.socket.Close()
	}()

	for {
		message, ok := <-c.send
		if !ok {
			log.Printf("Send channel closed for client in room %s", c.roomID)
			return
		}

		if message.IsFile {
			err := c.socket.WriteMessage(websocket.BinaryMessage, append([]byte{1}, message.FileData...))
			if err != nil {
				log.Printf("Write error (binary): %v", err)
				return
			}
			log.Printf("File sent successfully to client in room %s", c.roomID)
		} else {
			err := c.socket.WriteJSON(message)
			if err != nil {
				log.Printf("Write error (text): %v", err)
				return
			}
			log.Printf("Message sent successfully to client in room %s", c.roomID)
		}
	}
}

func (c *Client) ReadPump() {
	defer func() {
		room := roomManager.GetOrCreateRoom(c.roomID)
		room.mutex.Lock()
		delete(room.clients, c)
		room.mutex.Unlock()
		c.socket.Close()
	}()

	for {
		_, msgData, err := c.socket.ReadMessage()
		if err != nil {
			log.Printf("Read error: %v", err)
			break
		}

		if len(msgData) > 0 && msgData[0] == 1 {
			fileMessage := Message{
				RoomID:   c.roomID,
				Username: c.username,
				IsFile:   true,
				FileData: msgData[1:],
				FileName: "filename",
				FileType: "application/octet-stream",
			}

			room := roomManager.GetOrCreateRoom(c.roomID)
			room.BroadcastMessage(fileMessage)

		} else {
			var message Message
			err := json.Unmarshal(msgData, &message)
			if err != nil {
				log.Printf("Error unmarshaling message: %v", err)
				continue
			}
			message.Username = c.username
			message.RoomID = c.roomID
			log.Printf("Received message in room %s: %+v", c.roomID, message)

			room := roomManager.GetOrCreateRoom(c.roomID)
			room.BroadcastMessage(message)
		}
	}
}

func main() {
	http.HandleFunc("/ws", HandleWebSocket)

	log.Println("WebSocket server started on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
