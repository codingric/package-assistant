# ---- Build Stage ----
# This stage uses the official Go image to build the application.
# It runs on the CI runner's native platform ($BUILDPLATFORM) and
# cross-compiles for each target platform, so no emulation is needed here.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG VERSION
# Populated automatically by BuildKit for each requested platform.
ARG TARGETOS
ARG TARGETARCH

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum to download dependencies first.
# This leverages Docker's layer caching, so dependencies are only
# re-downloaded if go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code into the container
COPY . .

# Build the Go application, creating a static binary for the target platform.
# CGO_ENABLED=0 is crucial for building a static binary that can run in a minimal image.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-X main.version=${VERSION}" -a -installsuffix cgo -o /app/main .

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