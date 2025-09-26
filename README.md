Live Sequence Protocol (LSP).
LSP provides features that lie somewhere between UDP and TCP, but it also has features
not found in either protocol:
• Unlike UDP or TCP, it supports a client-server communication model.
• The server maintains connections between a number of clients, each of which is
identified by a numeric connection identifier.
• Communication between the server and a client consists of a sequence of discrete
messages in each direction.
• Message sizes are limited to fit within single UDP packets (around 1,000 bytes).
• Messages are sent reliably: a message sent over the network must be received exactly
once, and messages must be received in the same order they were sent.
• Message integrity is ensured: a message sent over the network will be rejected if
modified in transit.
• The server and clients monitor the status of their connections and detect when the
other side has become disconnected.

This project is designed for and tested on AFS cluster machines, though you may choose to
write and build your code locally as well.


### Running the tests

To test execute the following command from inside the
`src/github.com/cmu440/lsp` directory for each of the tests (where `TestName` is the
name of one of the 61 test cases, such as `TestBasic6` or `TestWindow1`):

```sh
go test -run=TestName
```

To ensure that previous tests don’t affect the outcome of later tests,
we recommend executing the tests individually (or in small batches, such as `go test -run=TestBasic` which will
execute all tests beginning with `TestBasic`) as opposed to all together using `go test`.

On some tests, we will also check your code for race conditions using Go’s race detector:

```sh
go test -race -run=TestName
```

## Part B

### Importing the `bitcoin` package

In order to use the starter code we provide in the `hash.go` and `message.go` files, use the
following `import` statement:

```go
import "github.com/cmu440/bitcoin"
```

Once you do this, you should be able to make use of the `bitcoin` package as follows:

```go
hash := bitcoin.Hash("thom yorke", 19970521)

msg := bitcoin.NewRequest("jonny greenwood", 200, 71010)
```

### Compiling the `client`, `miner` & `server` programs

To compile the `client`, `miner`, and `server` programs, use the `go install` command
as follows:

```bash
# Compile the client, miner, and server programs. The resulting binaries
# will be located in the $GOPATH/bin directory.
go install github.com/cmu440/bitcoin/client
go install github.com/cmu440/bitcoin/miner
go install github.com/cmu440/bitcoin/server

# Start the server, specifying the port to listen on.
$HOME/go/bin/server 6060

# Start a miner, specifying the server's host:port.
$HOME/go/bin/miner localhost:6060

# Start the client, specifying the server's host:port, the message
# "bradfitz", and max nonce 9999.
$HOME/go/bin/client localhost:6060 bradfitz 9999
```

Note that you will need to use the `os.Args` variable in your code to access the user-specified
command line arguments.




2. Start a godoc server by running the following command **inside** the `src/github.com/cmu440` directory:
```sh
godoc -http=:6060
```

3. While the server is running, navigate to [localhost:6060/pkg/github.com/cmu440](http://localhost:6060/pkg/github.com/cmu440) in a browser.
