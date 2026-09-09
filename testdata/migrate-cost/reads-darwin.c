// Count successful process read/pread bytes, including cache hits. The Go
// runtime exits without C destructors, so publish each total as it changes.
#include <unistd.h>
#include <stdint.h>
#include <stdlib.h>
#include <fcntl.h>
#include <pthread.h>

static uint64_t total;
static int counter = -1;
static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;

__attribute__((constructor)) static void begin(void) {
    const char *path = getenv("ROCA_COST_COUNTER");
    if (path) counter = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0600);
}

static void record(ssize_t count) {
    if (count <= 0 || counter < 0) return;
    pthread_mutex_lock(&lock);
    total += count;
    if (pwrite(counter, &total, sizeof(total), 0) != sizeof(total)) _exit(125);
    pthread_mutex_unlock(&lock);
}

static ssize_t measured_read(int fd, void *buffer, size_t count) {
    ssize_t result = read(fd, buffer, count);
    record(result);
    return result;
}

static ssize_t measured_pread(int fd, void *buffer, size_t count, off_t offset) {
    ssize_t result = pread(fd, buffer, count, offset);
    record(result);
    return result;
}

#define INTERPOSE(replacement, original) \
    __attribute__((used)) static struct { const void *r; const void *o; } \
    pair_##original __attribute__((section("__DATA,__interpose"))) = \
    { (const void *)&replacement, (const void *)&original };
INTERPOSE(measured_read, read)
INTERPOSE(measured_pread, pread)
