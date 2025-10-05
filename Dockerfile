# ---- Build Stage ----
# This stage uses the official Go image to build the application.
FROM golang:1.25-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum to download dependencies first.
# This leverages Docker's layer caching, so dependencies are only
# re-downloaded if go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code into the container
COPY . .

# Build the Go application, creating a static binary.
# CGO_ENABLED=0 is crucial for building a static binary that can run in a minimal image.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -a -installsuffix cgo -o /app/main .

# ---- Final Stage ----
# This stage uses a minimal Alpine image for the final container.
FROM alpine:latest

RUN apk --no-cache --update add tzdata

# Copy the pre-built binary from the builder stage
COPY --from=builder /app/main /app/main

# Expose port 8080 to allow external traffic
EXPOSE 8080

# Command to run the executable when the container starts
CMD ["/app/main"]