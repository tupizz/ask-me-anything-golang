package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/tupizz/ask-me-anything-server/internal/store/pgstore"
	"log/slog"
	"net/http"
	"sync"
)

type apiHandler struct {
	queries     *pgstore.Queries                                  // The database queries build with sqlc
	router      *chi.Mux                                          // The router used to define the API routes
	upgrader    websocket.Upgrader                                // Used to upgrade the HTTP connection to a WebSocket connection
	subscribers map[string]map[*websocket.Conn]context.CancelFunc // Map of room UUIDs to a map of WebSocket connections to cancel functions
	mutex       *sync.Mutex                                       // Mutex to protect the subscribers map
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

func NewAPIHandler(q *pgstore.Queries) http.Handler {
	api := apiHandler{
		queries:     q,
		subscribers: make(map[string]map[*websocket.Conn]context.CancelFunc),
		mutex:       &sync.Mutex{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // CORS policy for WebSockets
			},
		},
	}

	router := chi.NewRouter()
	// Middleware definitions
	router.Use(middleware.RequestID, middleware.Recoverer, middleware.Logger)

	// Handle CORS
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},                                   // Allowing all origins
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},        // adds support for OPTIONS method
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"}, // allows everything
		ExposedHeaders:   []string{"Link"},                                                    // allows the client to read the Link header from the response
		AllowCredentials: false,                                                               // this is set to true because we want to allow the client to send cookies
		MaxAge:           300,                                                                 // this means that the preflight request (OPTIONS) can be cached for 5 minutes
	}))

	// Webhook
	router.Get("/subscribe/{room_id}", api.handleWebhookSubscription)

	// REST API routes definitions
	router.Route("/api", func(r chi.Router) {
		// Rooms routes
		r.Route("/rooms", func(r chi.Router) {
			r.Post("/", api.handleCreateRoom)
			r.Get("/", api.handleGetRooms)

			// Messages in the room
			r.Route("/{room_id}/messages", func(r chi.Router) {
				r.Post("/", api.handleCreateRoomMessage)
				r.Get("/", api.handleGetRoomMessages)
			})

			r.Route("/{message_id}", func(r chi.Router) {
				r.Get("/", api.handleGetRoomMessageId)
				r.Patch("/react", api.handleReactRoomMessageId)
				r.Patch("/answer", api.handleAnswerRoomMessageId)
				r.Delete("/react", api.handleDeleteReactMessageId)
			})
		})
	})

	api.router = router
	return api
}

const (
	MessageKindMessageCreated = "message_created"
)

type MessageCreatedNotify struct {
	ID      string
	Message string
}

type Message struct {
	Kind   string `json:"kind"`
	Value  any    `json:"value"`
	RoomID string `json:"-"`
}

/**
 * Helper function to send JSON response to all clients in a room
 */
func (h apiHandler) notifyClients(msg Message) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	subscribers, ok := h.subscribers[msg.RoomID]
	if !ok || len(subscribers) == 0 {
		return
	}

	for conn, cancel := range subscribers {
		if err := conn.WriteJSON(msg); err != nil {
			slog.Error("failed to write message to client", "error", err)
			cancel()
		}
	}
}

/**
 * Room routes
 */
func (h apiHandler) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	type requestBody struct {
		Theme string `json:"theme"`
	}

	var requestBodyInput requestBody
	if err := json.NewDecoder(r.Body).Decode(&requestBodyInput); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	roomID, err := h.queries.InsertRoom(r.Context(), requestBodyInput.Theme)
	if err != nil {
		slog.Error("failed to insert room", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	slog.Info("room created", "room_id", roomID)

	type response struct {
		ID string `json:"id"`
	}

	sendJSON(w, response{ID: roomID.String()})
}
func (h apiHandler) handleGetRooms(w http.ResponseWriter, r *http.Request) {}

/**
 * Room Messages routes
 */
func (h apiHandler) handleCreateRoomMessage(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")

	// Check if RoomID is valid
	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	// Try finding room for the params.room_id
	_, err = h.queries.GetRoom(r.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "room not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	type requestBody struct {
		Message string `json:"message"`
	}

	var requestBodyInput requestBody
	if err := json.NewDecoder(r.Body).Decode(&requestBodyInput); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	newMessageUUID, err := h.queries.InsertMessage(r.Context(), pgstore.InsertMessageParams{
		RoomID:  roomID,
		Message: requestBodyInput.Message,
	})
	if err != nil {
		slog.Error("failed to insert message", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	sendJSON(w, response{ID: newMessageUUID.String()})

	/**
	 * Notify all clients in the room about the new message
	 * this will create a new goroutine to notify the clients
	 * that way we don't block the response
	 */
	go func() {
		h.notifyClients(Message{
			Kind: MessageKindMessageCreated,
			Value: MessageCreatedNotify{
				ID:      newMessageUUID.String(),
				Message: requestBodyInput.Message,
			},
			RoomID: rawRoomID,
		})
	}()
}

func (h apiHandler) handleGetRoomMessages(w http.ResponseWriter, r *http.Request) {}

/**
 * Room Message ID routes
 */
func (h apiHandler) handleGetRoomMessageId(w http.ResponseWriter, r *http.Request)     {}
func (h apiHandler) handleReactRoomMessageId(w http.ResponseWriter, r *http.Request)   {}
func (h apiHandler) handleDeleteReactMessageId(w http.ResponseWriter, r *http.Request) {}
func (h apiHandler) handleAnswerRoomMessageId(w http.ResponseWriter, r *http.Request)  {}

/**
 * Webhook
 */
func (h apiHandler) handleWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	// Try finding room for the params.room_id
	_, err = h.queries.GetRoom(r.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "room not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	/**
	 * Here we upgrade the HTTP connection to a WebSocket connection. This allows
	 * us to use the connection to send and receive messages in real-time.
	 */
	connection, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("failed to upgrade connection", "error", err)
		http.Error(w, "failed to upgrade connection", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())

	// mutex here to protect subscribers map
	h.mutex.Lock()
	// If the room ID is not in the subscribers map
	if _, ok := h.subscribers[rawRoomID]; !ok {
		// we need to create a new entry in the subscribers map
		h.subscribers[rawRoomID] = make(map[*websocket.Conn]context.CancelFunc)
	}
	slog.Info("new client connected", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
	// add the cancel function to the map
	h.subscribers[rawRoomID][connection] = cancel

	// Log all subscribers connected to this room
	slog.Info("subscribers connected to room", "room_id", rawRoomID, "count", len(h.subscribers[rawRoomID]))
	for conn := range h.subscribers[rawRoomID] {
		slog.Info("subscriber", "client_ip", conn.RemoteAddr().String())
	}
	// unlock the mutex
	h.mutex.Unlock()

	// Goroutine to handle WebSocket connection
	go func() {

		// Cleanup when the goroutine exits
		defer func() {
			cancel()
			h.mutex.Lock()
			delete(h.subscribers[rawRoomID], connection)
			h.mutex.Unlock()
			connection.Close()
			slog.Info("client disconnected", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
		}()

		// Loop to read messages from the client
		for {
			_, message, err := connection.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					slog.Info("client disconnected normally", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
				} else {
					slog.Warn("read error, client might have disconnected unexpectedly", "error", err)
				}
				return // Exit the goroutine and trigger cleanup
			}

			// Handle the message received from the client, convert bytes to string
			slog.Info("received message", "room_id", rawRoomID, "client_ip", r.RemoteAddr, "raw_message", string(message))

			//type webhookMessageInput struct {
			//	Message string `json:"message"`
			//}
			//
			//var jsonMessage webhookMessageInput
			//if err := json.Unmarshal(message, &jsonMessage); err != nil {
			//	slog.Error("failed to unmarshal message", "error", err)
			//	continue
			//}
			//
			//slog.Info("received message", "room_id", rawRoomID, "client_ip", r.RemoteAddr, "message", jsonMessage.Message)
		}
	}()

	// Keep the HTTP connection open as long as the WebSocket connection is active
	<-ctx.Done()

	println("\n\n\nENTROU AQUI\n\n\n")
	slog.Info("client disconnected", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
}
