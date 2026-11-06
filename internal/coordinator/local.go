package coordinator

// LocalClient binds daemon application use cases to their own coordinator.
// It executes commands in-process, without dialing any socket.
func (s *Server) LocalClient() *Client {
	return &Client{local: s, SocketPath: s.socketPath, BuildIdentity: s.buildIdentity}
}
