#!/usr/bin/ruby

# $ ./lein run -- test -w lin-kv-only --bin demo/ruby/lin_kv_sdp.rb --time-limit 10 --node-count 3 --rate 1 --concurrency 2n --key-count 1

# A linearizable key-value store which works by trying to reach linearizability
# through consensus on each operation with a Maelstrom-provided async consensus
# algorithm.

require_relative 'node.rb'
require_relative 'promise.rb'

class LinKVNode
  def initialize
    @node = Node.new

    # Local node unique proposal id.
    @node_proposed_id = 0
    # Local undecided proposals cache.
    @proposals = {}
    @queue = Queue.new
    # Learned node_id -> max learned pid.
    @learned = {}

    @storage = {}

    @lock = Monitor.new
    @ctype = "sdp-con"

    Thread.new { consensus_loop }

    @node.on "read" do |msg|
      key = msg[:body][:key]
      STDERR.puts("- read req key(#{key})")
      loop do
        v1, ok = preread key
        if ok
          STDERR.puts("+ read done key(#{key}) = ""#{v1}""")
          if v1.nil?
            @node.reply! msg, { type: "error", code: 20, text: "key does not exist" }
          else
            @node.reply! msg, { type: "read_ok", value: v1 }
          end
          break
        else
          STDERR.puts("- read key(#{key}) = ""#{v1}"" failed")
          sleep 0.1
        end
      end
    end

    @node.on "internal_read" do |msg|
      key = msg[:body][:key]
      val = @storage[key]
      @node.reply! msg, { type: "internal_read_ok", value: val }
    end

    @node.on "write" do |msg|
      STDERR.puts("- write req key(#{msg[:body][:key]}) = #{msg[:body][:value]}")
      consensus! msg
      @node.reply! msg, { type: "write_ok" }
    end

    @node.on "learn" do |msg|
      learn msg[:body][:value].to_hs!
    end
  end

  # Reads the value from all nodes and selects quorum.
  # Returns [value: any, read: bool].
  def preread(key)
    data = try_internal_read key
    quorum = @node.node_ids.length / 2 + 1
    STDERR.puts("- preread key(#{key}) = ""#{data}""")
    # XXX: is successful quorum linearizable?
    select_read_val(data, quorum)
  end

  def try_internal_read(key)
    data = {}
    expected = @node.node_ids.length

    lock = Mutex.new
    cvar = ConditionVariable.new

    req = { type: "internal_read", key: key }

    @node.node_ids.each do |node|
        @node.rpc! node, req do |msg| 
        src = msg[:src]
        val = msg[:body][:value]

        data[src] = val

        # TODO: leave earlier.
        lock.synchronize do
          expected -= 1 if expected > 0
          cvar.broadcast
        end
      end
    end

    loop do
      break if expected == 0
      lock.synchronize do
        cvar.wait lock, 1
      end
    end

    data
  end

  def select_read_val(data, quorum)
    values = {}

    data.each do |src, val|
      values[val] ||= 0
      values[val] += 1
    end

    values.each do |val, count|
      if count >= quorum
        return [val, true]
      end
    end

    # no value was read.
    [nil, false]
  end

  def proposal!(val)
    @node_proposed_id += 1

    { node_id: @node.node_id, pid: @node_proposed_id, value: val.to_hs! }
  end

  def consensus!(msg)
    promise = Promise.new

    # ensure queued pids are monotonically increasing.
    @lock.synchronize do
      proposal = proposal! msg
      pid = proposal[:pid]
      @proposals[pid] = { promise: promise, proposal: proposal }
      @queue.push pid
    end

    promise.await
  end

  def consensus_loop
    loop do
      pid = @queue.pop
      if pid <= @learned[@node.node_id].to_i
        if @proposals[pid]
          STDERR.puts("consensus loop: found already learned pid #{pid} (<= #{@learned[@node.node_id]}) with present proposal...")
          @proposals.delete(pid)
        end

        next
      end

      # retry proposal until learned. learn is async.
      loop do
        tuple = @proposals[pid]
        break unless tuple

        req = { type: "propose", value: tuple[:proposal] }
        res = @node.sync_rpc! @ctype, req
        if res[:body][:type] == "error" && res[:body][:text] == "already decided"
          req = { type: "reset" }
          res = @node.sync_rpc! @ctype, req
          continue
        elsif res[:body][:type] != "propose_ok"
          abort "Proposal response invalid: #{res}"
        end

        res = @node.sync_rpc! @ctype, { type: "decide" }
        if res[:body][:type] == "error" && res[:body][:text] == "already decided"
          req = { type: "reset" }
          res = @node.sync_rpc! @ctype, req
          continue
        elsif res[:body][:type] == "decide_ok"
          # If something was decided broadcast it to be learned.
          # It may be another unknown proposal.
          decided = res[:body][:value].to_hs!
          msg = { type: "learn", value: decided }
          @node.node_ids.each do |n|
            @node.send! n, msg
          end
          # @proposals cleanup will be done in learn().
          break
        end
      end
    rescue StandardError => err
      STDERR.puts("consensus loop: did not see requests in 5s... (#{err})")
    end
  end

  def learn(proposal)
    pid = proposal[:pid]

    # learning must be monotonically consecutive with decided values consumption.
    @lock.synchronize do
      # ignore possible reordering for now that will make it to lose decided values.
      if pid <= @learned[proposal[:node_id]].to_i
        return
      end

      @learned[proposal[:node_id]] = pid

      STDERR.puts("learn: #{proposal}, node_id = #{proposal[:node_id]}, pid = #{proposal[:pid]}")

      promise = nil

      if @node.node_id == proposal[:node_id]
        abort "Learned proposal is missing in the cache" unless @proposals[pid]
        promise = @proposals[pid][:promise]
        @proposals.delete proposal[:pid]
      end

      apply_learned!(promise, pid, proposal[:value])
    end
  end

  def apply_learned!(promise, pid, msg)
    body = msg[:body]
    case body[:type]
    when "write"
      @storage[body[:key]] = body[:value]
      STDERR.puts("+ write_ok key(#{body[:key]}) = #{body[:value]}")
      promise.deliver! true if promise
    when "read"
      val = @storage[body[:key]]
      STDERR.puts("+ read_val key(#{body[:key]}) = #{val}")
      promise.deliver! val if promise
    else
      abort "unknown operation: #{msg}"
    end
  end

  def main!
    @node.main!
  end
end

class Hash
  # to_hs! method to convert a Hash of objects into the Hash with symbolic keys.
  def to_hs!
    keys.each do |key|
      value = delete(key)
      self[key.to_sym] = value.is_a?(Hash) ? value.to_hs! : value
    end
    self
  end
end

LinKVNode.new.main!
