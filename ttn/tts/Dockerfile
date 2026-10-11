# Copyright 2026 Abhinav Gautam
# SPDX-License-Identifier: Apache-2.0
#
# The reference runner for the codec examples: The Things Stack v3.36.2
# (Apache-2.0, go.thethings.network/lorawan-stack/v3) and its
# pkg/scripting/javascript, the runner the lorawan-devices validator uses,
# driven by oracle.go (ours) in batch. Built with network access.
#
#   docker build -t sigma-jscodec-tts:3.36.2 .

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY oracle.go ./main.go
RUN sed -i "/^\/\/go:build ignore/d" main.go \
 && go mod init oracle \
 && go mod edit -replace github.com/mitchellh/mapstructure=github.com/TheThingsIndustries/mapstructure@v0.0.0-20230413130846-941bcd1deec3 \
 && go get go.thethings.network/lorawan-stack/v3@v3.36.2 \
 && go mod tidy \
 && CGO_ENABLED=0 go build -o /oracle .

FROM alpine:3.20
COPY --from=build /oracle /oracle
ENTRYPOINT ["/oracle"]
