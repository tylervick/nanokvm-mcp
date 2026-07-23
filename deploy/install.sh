#!/bin/sh
# Install the sidecar on a NanoKVM. Run from a machine with scp/ssh access.
# Usage: HOST=root@nanokvm ./deploy/install.sh
set -e
: "${HOST:?set HOST=root@<nanokvm>}"
SSH="ssh -o PubkeyAuthentication=no -o PreferredAuthentications=password"
SCP="scp -o PubkeyAuthentication=no -o PreferredAuthentications=password"

echo "Building riscv64 binary..."
mise run build

echo "Creating directories..."
$SSH "$HOST" 'mkdir -p /root/nanokvm-mcp /data/nanokvm-mcp'

echo "Copying binary and init script..."
$SCP dist/nanokvm-mcp "$HOST":/root/nanokvm-mcp/nanokvm-mcp
$SCP deploy/S96nanokvm-mcp "$HOST":/etc/init.d/S96nanokvm-mcp
$SSH "$HOST" 'chmod +x /root/nanokvm-mcp/nanokvm-mcp /etc/init.d/S96nanokvm-mcp'

echo "Writing config template if absent..."
$SSH "$HOST" 'test -f /root/nanokvm-mcp/nanokvm-mcp.env || cat > /root/nanokvm-mcp/nanokvm-mcp.env <<EOF
NANOKVM_HOST=127.0.0.1
NANOKVM_MCP_BIND=127.0.0.1:8080
# NANOKVM_MCP_TOKEN=   # set a fixed bearer token, or read the generated one from the log
# NANOKVM_MCP_READONLY=true
EOF'

echo "Starting..."
$SSH "$HOST" '/etc/init.d/S96nanokvm-mcp restart'
echo "Done. Bearer token (if generated) is in /data/nanokvm-mcp/daemon.log"
