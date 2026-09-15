/* Lost-reply fault for native IPC clients, loaded with LD_PRELOAD (Linux).
 *
 * Contract (shared with go/ and python/): the first request frame whose
 * "method" field equals OA_LOST_REPLY_METHOD (default "Submit") is delivered
 * unchanged. The service processes it and sends its reply. The complete reply
 * frame is received, then discarded, and the caller observes a connection reset.
 * Nothing is invented and no extra request is sent. The fault fires once per
 * process. OA_LOST_REPLY_MARKER, when set, receives the discarded byte counts.
 *
 * Applies to any process whose frames pass through libc send/recv: the C++
 * facade, the Python binding over libabstraction_ipc.so, Rust over the C ABI
 * and the Node-API addon. Go uses its own socket calls; use go/ instead.
 *
 *   gcc -shared -fPIC -O2 -o lostreply.so lostreply.c -ldl
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <poll.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/types.h>

static ssize_t (*real_send)(int, const void*, size_t, int);
static ssize_t (*real_recv)(int, void*, size_t, int);
static int marked = -1;
static int fired = 0;

static void init(void) {
    if (!real_send) real_send = dlsym(RTLD_NEXT, "send");
    if (!real_recv) real_recv = dlsym(RTLD_NEXT, "recv");
}

/* True when bytes contain  "method" <ws> : <ws> "<target>"  as a JSON member. */
static int names_method(const unsigned char* bytes, size_t length, const char* target) {
    static const char key[] = "\"method\"";
    size_t key_length = sizeof key - 1, target_length = strlen(target);
    const unsigned char* at = bytes;
    const unsigned char* end = bytes + length;
    while ((at = memmem(at, (size_t)(end - at), key, key_length)) != NULL) {
        const unsigned char* p = at + key_length;
        while (p < end && (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n')) p++;
        if (p < end && *p == ':') {
            p++;
            while (p < end && (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n')) p++;
            if ((size_t)(end - p) >= target_length + 2 && p[0] == '"' &&
                memcmp(p + 1, target, target_length) == 0 && p[target_length + 1] == '"') return 1;
        }
        at += key_length;
    }
    return 0;
}

static int read_exact(int fd, unsigned char* buffer, size_t want, size_t* got) {
    *got = 0;
    while (*got < want) {
        struct pollfd ready = {fd, POLLIN, 0};
        int polled = poll(&ready, 1, 20000);
        if (polled < 0 && errno == EINTR) continue;
        if (polled <= 0) return 0;
        ssize_t n = real_recv(fd, buffer + *got, want - *got, 0);
        if (n < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK)) continue;
        if (n <= 0) return 0;
        *got += (size_t)n;
    }
    return 1;
}

ssize_t send(int fd, const void* buffer, size_t length, int flags) {
    init();
    ssize_t sent = real_send(fd, buffer, length, flags);
    if (sent > 0 && !fired) {
        const char* method = getenv("OA_LOST_REPLY_METHOD");
        if (names_method(buffer, (size_t)sent, method && *method ? method : "Submit")) marked = fd;
    }
    return sent;
}

ssize_t recv(int fd, void* buffer, size_t length, int flags) {
    init();
    if (fd != marked || fired) return real_recv(fd, buffer, length, flags);
    fired = 1;
    marked = -1;
    unsigned char header[4];
    size_t got = 0, body = 0;
    if (read_exact(fd, header, 4, &got)) {
        uint32_t size = ((uint32_t)header[0] << 24) | ((uint32_t)header[1] << 16) | ((uint32_t)header[2] << 8) | header[3];
        unsigned char scratch[65536];
        while (body < size) {
            size_t want = size - body > sizeof scratch ? sizeof scratch : size - body;
            size_t part = 0;
            int complete = read_exact(fd, scratch, want, &part);
            body += part;
            if (!complete) break;
        }
    }
    const char* marker = getenv("OA_LOST_REPLY_MARKER");
    if (marker) {
        FILE* out = fopen(marker, "w");
        if (out) {
            fprintf(out, "discarded %zu header bytes and %zu reply body bytes after %s\n", got, body,
                    getenv("OA_LOST_REPLY_METHOD") && *getenv("OA_LOST_REPLY_METHOD") ? getenv("OA_LOST_REPLY_METHOD") : "Submit");
            fclose(out);
        }
    }
    errno = ECONNRESET;
    return -1;
}
