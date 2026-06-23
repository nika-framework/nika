# Raw Body

> **Coming Soon** — Built-in raw body access is not yet implemented.

Sometimes you need access to the raw request body (e.g., for webhook signature verification).

## Current Alternative

Use Gin's context:

```go
func (ctrl *WebhookController) Handle(c *gin.Context) {
    // Read raw body
    bodyBytes, err := io.ReadAll(c.Request.Body)
    if err != nil {
        response.BadRequest(c, "READ_ERROR", err.Error())
        return
    }
    defer c.Request.Body.Close()

    // Verify webhook signature
    signature := c.GetHeader("X-Signature")
    if !verifySignature(signature, bodyBytes) {
        response.BadRequest(c, "INVALID_SIGNATURE", "Webhook signature verification failed")
        return
    }

    // Parse body
    var payload WebhookPayload
    json.Unmarshal(bodyBytes, &payload)
}
```

## Status

| Feature | Status |
|---------|--------|
| Raw body decorator | ⏳ Planned |
| Body buffering middleware | ⏳ Planned |
