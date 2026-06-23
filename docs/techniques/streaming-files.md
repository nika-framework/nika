# Streaming Files

> **Coming Soon** — Built-in streaming utilities are not yet implemented.

Use Gin's built-in streaming capabilities for serving large files or real-time data.

## Current Approach

### Stream a File

```go
func (ctrl *FileController) StreamVideo(c *gin.Context) {
    file := c.Param("name")
    filePath := "./videos/" + file

    c.File(filePath)
}
```

### Stream with Range Support (Video/Audio)

```go
func (ctrl *FileController) StreamWithRange(c *gin.Context) {
    filePath := "./videos/video.mp4"

    f, err := os.Open(filePath)
    if err != nil {
        c.AbortWithStatus(404)
        return
    }
    defer f.Close()

    stat, _ := f.Stat()

    c.DataFromReader(
        http.StatusOK,
        stat.Size(),
        "video/mp4",
        f,
        map[string]string{
            "Content-Disposition": fmt.Sprintf(`inline; filename="%s"`, stat.Name()),
        },
    )
}
```

### Stream JSON Response

```go
func (ctrl *UserController) StreamUsers(c *gin.Context) {
    c.Header("Content-Type", "application/json")

    users := ctrl.service.FindAllStream()
    encoder := json.NewEncoder(c.Writer)

    c.Writer.Write([]byte("["))
    first := true

    for user := range users {
        if !first {
            c.Writer.Write([]byte(","))
        }
        encoder.Encode(user)
        first = false
    }

    c.Writer.Write([]byte("]"))
}
```

## Status

| Feature | Status |
|---------|--------|
| File streaming | ✅ Available via Gin |
| Range request support | ✅ Available via Gin |
| SSE (Server-Sent Events) | ⏳ Planned |
| Chunked transfer encoding | ⏳ Planned |
