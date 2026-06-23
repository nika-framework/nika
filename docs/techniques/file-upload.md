# File Upload

> **Coming Soon** — Built-in file upload utilities are not yet implemented.

Since Nika is built on Gin, you can use Gin's built-in file upload capabilities.

## Current Approach

### Single File Upload

```go
func (ctrl *UploadController) Upload(c *gin.Context) {
    file, err := c.FormFile("file")
    if err != nil {
        response.BadRequest(c, "UPLOAD_ERROR", err.Error())
        return
    }

    // Save the file
    dst := "./uploads/" + file.Filename
    if err := c.SaveUploadedFile(file, dst); err != nil {
        response.JSONError(c, 500, "SAVE_ERROR", err.Error())
        return
    }

    response.OkByMsg(c, "File uploaded successfully")
}
```

### Multiple File Upload

```go
func (ctrl *UploadController) UploadMultiple(c *gin.Context) {
    form, err := c.MultipartForm()
    if err != nil {
        response.BadRequest(c, "UPLOAD_ERROR", err.Error())
        return
    }

    files := form.File["files"]
    for _, file := range files {
        dst := "./uploads/" + file.Filename
        c.SaveUploadedFile(file, dst)
    }

    response.OkByMsg(c, fmt.Sprintf("%d files uploaded", len(files)))
}
```

### File Size Limit

```go
func (ctrl *UploadController) Upload(c *gin.Context) {
    // Limit upload size to 10 MB
    c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)

    file, err := c.FormFile("file")
    // ...
}
```

## Status

| Feature | Status |
|---------|--------|
| Single file upload | ✅ Available via Gin |
| Multiple file upload | ✅ Available via Gin |
| File size limits | ✅ Available via Gin |
| File type validation | ⏳ Planned |
| S3/Cloud storage integration | ⏳ Planned |
