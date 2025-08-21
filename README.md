# Threads Video Downloader Backend

A Go backend service for extracting video and image URLs from Threads posts using browser automation.

## Features

- **Multi-Strategy Extraction**: 4-tier fallback approach for maximum reliability
- **Browser Automation**: Uses Chrome/Chromium with Rod library
- **CORS Support**: Ready for frontend integration
- **Security**: Domain-restricted download proxy
- **Metadata Extraction**: Author info, titles, duration, multiple quality URLs

## API Endpoints

### POST /api/extract
Extract media URL from a Threads post.

**Request:**
```json
{
  "url": "https://www.threads.net/@username/post/POST_ID"
}
```

**Response:**
```json
{
  "mediaUrl": "https://...",
  "mediaType": "video|image",
  "success": true,
  "videoId": "123456",
  "title": "Post title",
  "duration": 30,
  "author": "@username",
  "authorName": "Display Name",
  "videoUrls": {
    "hd": "https://...",
    "sd": "https://..."
  },
  "metadata": {
    "video_id": "123456",
    "duration": "30 seconds"
  }
}
```

### GET /api/download
Proxy download for media files.

**Parameters:**
- `url`: Media URL to download
- `filename`: Optional filename for download

### GET /health
Health check endpoint.

## Environment Variables

- `CHROME_PATH`: Path to Chrome/Chromium binary (auto-detected if not set)
- `HTTP_PROXY`: Proxy server URL (optional)
- `PORT`: Server port (default: 8080)

## Installation

1. **Install Go** (1.21 or later)

2. **Install dependencies:**
   ```bash
   go mod tidy
   ```

3. **Install Chrome/Chromium:**
   - **Windows**: Install Google Chrome
   - **Linux**: `sudo apt install chromium-browser`
   - **macOS**: Install Google Chrome or `brew install chromium`

4. **Run the server:**
   ```bash
   go run main.go
   ```

## Usage

The server will start on port 8080 by default. You can test it with:

```bash
curl -X POST http://localhost:8080/api/extract \
  -H "Content-Type: application/json" \
  -d '{"url":"https://www.threads.net/@username/post/POST_ID"}'
```

## Docker Support

Create a `Dockerfile`:

```dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o main .

FROM alpine:latest
RUN apk add --no-cache chromium ca-certificates
ENV CHROME_PATH=/usr/bin/chromium-browser

WORKDIR /app
COPY --from=builder /app/main .

EXPOSE 8080
CMD ["./main"]
```

## Security Considerations

- Only allows downloads from trusted domains (cdninstagram.com, fbcdn.net, etc.)
- Implements request timeouts and panic recovery
- Uses headless browser with security flags
- CORS headers configured for web frontend integration

## Performance

- Browser instance reuse for better performance
- Optimized timeouts and element waiting
- Multiple extraction strategies with fast fallbacks
- Efficient regex pattern matching

## Legal Notice

This tool is for educational purposes. Users are responsible for complying with:
- Threads/Meta Terms of Service
- Copyright laws
- Fair use policies
- Rate limiting and respectful usage

## Troubleshooting

**Browser launch fails:**
- Set `CHROME_PATH` environment variable
- Install Chrome/Chromium
- Check system permissions

**Extraction fails:**
- Verify URL format: `/@username/post/POST_ID`
- Check if post is public
- Some content may be geo-restricted

**High memory usage:**
- Browser instances consume ~100-200MB each
- Consider implementing connection pooling for high traffic

