FROM alpine:3.22
RUN apk add --no-cache bash git tmux file ripgrep coreutils
RUN git config --system init.defaultBranch main
WORKDIR /workspace
CMD ["sh", "-c", "while :; do sleep 3600; done"]
