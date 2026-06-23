# WebSockets Gateways

> **Coming Soon** — WebSocket support is not yet implemented.

WebSockets enable real-time, bidirectional communication between clients and servers.

## Planned Design

```go
// Planned API (subject to change)
type WebSocketGateway struct {
    Hub    *Hub
    OnConnect    func(client *Client)
    OnDisconnect func(client *Client)
}

type Message struct {
    Event string      `json:"event"`
    Data  interface{} `json:"data"`
}
```

## Current Alternative

Use [gorilla/websocket](https://github.com/gorilla/websocket) with Gin:

```bash
go get github.com/gorilla/websocket
```

```go
import (
    "github.com/gin-gonic/gin"
    "github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool { return true },
}

func (ctrl *ChatController) HandleWS(c *gin.Context) {
    conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
    if err != nil {
        return
    }
    defer conn.Close()

    for {
        _, message, err := conn.ReadMessage()
        if err != nil {
            break
        }
        // Handle message
        conn.WriteMessage(websocket.TextMessage, message)
    }
}
```

## Status

| Feature | Status |
|---------|--------|
| WebSocket gateway | ⏳ Planned |
| Room-based messaging | ⏳ Planned |
| Event broadcasting | ⏳ Planned |
| WebSocket adapters | ⏳ Planned |

!!! info "Want to contribute?"
    WebSocket support is open for contribution.
