// Lab-only model read counter. It counts returned bytes, including cache hits.
// A missing/unreadable counter is an error in the consumer, never zero.
#include <unistd.h>
#include <stdint.h>
#include <stdlib.h>
#include <fcntl.h>
#include <pthread.h>
#include <string.h>
#include <sys/param.h>
#include <sys/stat.h>
#include <stdio.h>

static uint64_t total;
static int counter = -1;
static const char *model;
static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;

__attribute__((constructor)) static void begin(void) {
    const char *path = getenv("ROCA_D6_COUNTER");
    model = getenv("ROCA_D6_MODEL");
    if (!path || !model) return;
    char owned[MAXPATHLEN];
    if (snprintf(owned, sizeof(owned), "%s.%d", path, getpid()) >= sizeof(owned)) _exit(125);
    counter = open(owned, O_WRONLY | O_CREAT | O_EXCL, 0600);
    if (counter < 0 || pwrite(counter, &total, sizeof(total), 0) != sizeof(total)) _exit(125);
}

static void record(int fd, ssize_t count) {
    char path[MAXPATHLEN];
    if (count <= 0 || counter < 0) return;
    struct stat st;
    if (fstat(fd, &st) < 0) _exit(126);
    if (!S_ISREG(st.st_mode)) return;
    if (fcntl(fd, F_GETPATH, path) < 0) _exit(126);
    size_t length = strlen(model);
    if (strcmp(path, model) != 0 && !(strncmp(path, model, length) == 0 && strcmp(path + length, ".partial") == 0)) return;
    pthread_mutex_lock(&lock);
    total += count;
    if (pwrite(counter, &total, sizeof(total), 0) != sizeof(total)) _exit(125);
    pthread_mutex_unlock(&lock);
}
static ssize_t measured_read(int fd, void *buffer, size_t count) {
    ssize_t result = read(fd, buffer, count);
    record(fd, result);
    return result;
}
static ssize_t measured_pread(int fd, void *buffer, size_t count, off_t offset) {
    ssize_t result = pread(fd, buffer, count, offset);
    record(fd, result);
    return result;
}
#define INTERPOSE(replacement, original) \
    __attribute__((used)) static struct { const void *r; const void *o; } \
    pair_##original __attribute__((section("__DATA,__interpose"))) = \
    { (const void *)&replacement, (const void *)&original };
INTERPOSE(measured_read, read)
INTERPOSE(measured_pread, pread)
