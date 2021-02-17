# Maelstrom

Maelstrom is a workbench for learning distributed systems by writing your own,
tiny implementations of distributed algorithms. It's used as a part of a
distributed systems workshop by [Jepsen](https://jepsen.io/training).

## Overview

Maelstrom is a [Clojure](https://clojure.org/) program which runs on the [Java
Virtual Machine](https://en.wikipedia.org/wiki/Java_virtual_machine). It uses
the [Jepsen](https://github.com/jepsen-io/jepsen) testing library to test toy
implementations of distributed systems. Maelstrom provides standardized tests
for things like "a commutative set" or "a linearizable key-value store", and
lets you learn by writing implementations which those test suites can
exercise.

Writing "real" distributed systems involves a lot of busywork: process
management, networking, and message serialization are complex, full of edge
cases, and difficult to debug across languages. In addition, running a full
cluster of virtual machines connected by a real IP network is tricky for many
users. Maelstrom strips these problems away so you can focus on the algorithmic
essentials: process state, transitions, and messages.

The "nodes" in a Maelstrom test are simply programs, written in any language.
Nodes read "network" messages as JSON from STDIN, write JSON "network" messages
to STDOUT, and do their logging to STDERR. Maelstrom runs those nodes as
processes on your local machine, and connects them via a simulated network.
Maelstrom runs a collection of simulated network clients which make requests to
those nodes, receive responses, and records a history of those operations. At
the end of a test run, Maelstrom analyzes that history to identify safety
violations.

This allows learners to write their nodes in whatever language they are most
comfortable with, without having to worry about discovery, network
communication, daemonization, writing their own distributed test harness, and
so on. It also means that Maelstrom can perform sophisticated fault injection:
dropping, delaying, duplicating, and reordering network messages.

## Documentation

Start by skimming the [Protocol](doc/protocol.md) docs, which define
Maelstrom's "network" protocol, message structure, and error handling.

The [Workload](doc/workload) docs describe the various kinds of workloads that
Maelstrom can test, and define the messages involved in that particular
workload.

The Maelstrom Guide will take you through writing several different types of
distributed algorithms using Maelstrom. We begins by setting up Maelstrom and
its dependencies, write our own tiny echo server, and move on to more
sophisticated workloads.

- [Chapter 1: Getting Ready](doc/01-getting-ready/index.md)
- [Chapter 2: Echo](doc/02-echo/index.md)
- [Chapter 5: Raft](doc/05-raft/index.md)

## CLI Options

A full list of options is available by running `java -jar maelstrom.jar test
--help`. The important ones are:

- `--node NODE-NAME`: Specify node names by hand

To get more information, use:

- `--log-stderr`: Show STDERR output from each node in the Maelstrom log
- `--log-net-send`: Log messages as they are sent into the network
- `--log-net-recv`: Log messages as they are received by nodes

To make tests more or less aggressive, use:

- `--concurrency INT`: Number of clients to run concurrently
- `--rate FLOAT`: Approximate number of requests per second per client
- `--time-limit SECONDS`: How long to run tests for
- `--latency MILLIS`: Approximate simulated network latency, during normal
  operations.

SSH options are unused; Maelstrom runs entirely on the local node.

## License

Copyright © 2017, 2020 Kyle Kingsbury & Jepsen, LLC

Distributed under the Eclipse Public License either version 1.0 or (at
your option) any later version.
