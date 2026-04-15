#!/bin/bash

PIDFILE="./server.pid"
BINARY="./server"
CONFIG="./config.yaml"
LOG_OUT="./server.log"
LOG_ERR="./server.log.wf"

rotate_logs() {
    TS=$(date +%Y%m%d_%H%M%S)
    [ -s "$LOG_OUT" ] && mv "$LOG_OUT" "${LOG_OUT}.${TS}"
    [ -s "$LOG_ERR" ] && mv "$LOG_ERR" "${LOG_ERR}.${TS}"
    # Keep only the 10 most recent rotated logs
    ls -t "${LOG_OUT}".* 2>/dev/null | tail -n +11 | xargs rm -f
    ls -t "${LOG_ERR}".* 2>/dev/null | tail -n +11 | xargs rm -f
}

start() {
    if [ -f "$PIDFILE" ]; then
        PID=$(cat "$PIDFILE")
        if kill -0 "$PID" 2>/dev/null; then
            echo "Already running (pid $PID)"
            exit 0
        fi
        rm -f "$PIDFILE"
    fi

    rotate_logs
    nohup "$BINARY" -config "$CONFIG" >"$LOG_OUT" 2>"$LOG_ERR" &
    PID=$!
    echo $PID > "$PIDFILE"
    echo "Started (pid $PID)"
}

stop() {
    if [ ! -f "$PIDFILE" ]; then
        echo "Not running"
        exit 0
    fi

    PID=$(cat "$PIDFILE")
    if ! kill -0 "$PID" 2>/dev/null; then
        echo "Not running (stale pid $PID)"
        rm -f "$PIDFILE"
        exit 0
    fi

    kill "$PID"
    # Wait up to 10s for clean shutdown
    for i in $(seq 1 10); do
        if ! kill -0 "$PID" 2>/dev/null; then
            break
        fi
        sleep 1
    done

    if kill -0 "$PID" 2>/dev/null; then
        kill -9 "$PID"
        echo "Force killed (pid $PID)"
    else
        echo "Stopped (pid $PID)"
    fi

    rm -f "$PIDFILE"
}

status() {
    if [ ! -f "$PIDFILE" ]; then
        echo "Not running"
        exit 1
    fi

    PID=$(cat "$PIDFILE")
    if kill -0 "$PID" 2>/dev/null; then
        echo "Running (pid $PID)"
    else
        echo "Not running (stale pid $PID)"
        rm -f "$PIDFILE"
        exit 1
    fi
}

case "$1" in
    start)   start ;;
    stop)    stop ;;
    restart) stop; sleep 1; start ;;
    status)  status ;;
    *)
        echo "Usage: $0 {start|stop|restart|status}"
        exit 1
        ;;
esac
