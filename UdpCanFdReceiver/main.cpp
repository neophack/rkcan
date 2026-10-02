#include <cstdio>
#include <cstdint>
#include <cstring>
#include <cstdlib>
#include <ctime>
#include <chrono>
#include <csignal>

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#pragma comment(lib, "ws2_32.lib")
typedef int socklen_t;
#else
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <unistd.h>
#include <errno.h>
#endif

static volatile int g_running = 1;

static void sigHandler(int sig)
{
    (void)sig;
    g_running = 0;
}

#define UDP_PORT 6000
#define RECV_BUF_SIZE (64 * 1024)

/* Optional flag bits in the DLC byte (sender started with -udpflags).
 * Low nibble is always the DLC (0..15). */
#define DLC_MASK     0x0F
#define DLC_FLAG_FD  0x10
#define DLC_FLAG_BRS 0x20
#define DLC_FLAG_ESI 0x40

static uint32_t crc32(const uint8_t *data, uint32_t len)
{
    uint32_t crc = 0xFFFFFFFF;
    for (uint32_t i = 0; i < len; i++) {
        crc ^= data[i];
        for (int j = 0; j < 8; j++) {
            if (crc & 1)
                crc = (crc >> 1) ^ 0xEDB88320;
            else
                crc >>= 1;
        }
    }
    return crc ^ 0xFFFFFFFF;
}

static void formatUtcUs(uint64_t utcUs, char *out, size_t outLen)
{
    time_t sec = (time_t)(utcUs / 1000000ULL);
    uint32_t fracUs = (uint32_t)(utcUs % 1000000ULL);
    struct tm *tm = gmtime(&sec);
    if (tm) {
        snprintf(out, outLen, "%04d-%02d-%02d %02d:%02d:%02d.%06u",
                 tm->tm_year + 1900, tm->tm_mon + 1, tm->tm_mday,
                 tm->tm_hour, tm->tm_min, tm->tm_sec, fracUs);
    } else {
        snprintf(out, outLen, "%llu us", (unsigned long long)utcUs);
    }
}

int main(int argc, char *argv[])
{
    int port = UDP_PORT;
    if (argc > 1) port = atoi(argv[1]);

#ifdef _WIN32
    WSADATA wsa;
    if (WSAStartup(MAKEWORD(2, 2), &wsa) != 0) {
        fprintf(stderr, "WSAStartup failed: %d\n", WSAGetLastError());
        return 1;
    }
#endif

    printf("=== CAN FD UDP Receiver ===\n");
    printf("Listening on port %d ...\n", port);

#ifdef _WIN32
    SOCKET sock = socket(AF_INET, SOCK_DGRAM, 0);
    if (sock == INVALID_SOCKET) {
        fprintf(stderr, "socket create failed\n");
        return 1;
    }
#else
    int sock = socket(AF_INET, SOCK_DGRAM, 0);
    if (sock < 0) {
        fprintf(stderr, "socket create failed\n");
        return 1;
    }
#endif

    int rcvbuf = 4 * 1024 * 1024;
    setsockopt(sock, SOL_SOCKET, SO_RCVBUF, (const char *)&rcvbuf, sizeof(rcvbuf));

    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_port = htons(port);
    addr.sin_addr.s_addr = INADDR_ANY;

    if (bind(sock, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        fprintf(stderr, "bind failed\n");
        return 1;
    }

    printf("Bound! Waiting for data...\n\n");

    signal(SIGINT, sigHandler);

#ifdef _WIN32
    DWORD tv = 500;
    setsockopt(sock, SOL_SOCKET, SO_RCVTIMEO, (const char *)&tv, sizeof(tv));
#else
    struct timeval tv;
    tv.tv_sec = 0;
    tv.tv_usec = 500000;
    setsockopt(sock, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
#endif

    uint32_t pktCount = 0;
    uint32_t crcErrCount = 0;
    uint32_t syncErrCount = 0;
    uint32_t lastSeq = 0;
    uint32_t seqGapCount = 0;
    uint32_t fdCount = 0;
    uint32_t brsCount = 0;
    uint64_t lastUtcUs = 0;
    uint32_t lastMcuRelUs = 0;
    uint64_t lastQnxUtcUs = 0;

    uint8_t buf[RECV_BUF_SIZE];
    auto lastReport = std::chrono::steady_clock::now();

    uint32_t lastPrintedCount = 0;

    while (g_running) {
        int n = recvfrom(sock, (char *)buf, RECV_BUF_SIZE, 0, NULL, NULL);
        if (n <= 0) {
#ifdef _WIN32
            if (WSAGetLastError() == WSAETIMEDOUT) {
#else
            if (errno == EAGAIN || errno == EWOULDBLOCK) {
#endif
                if (pktCount > lastPrintedCount) {
                    char qnxStr[64];
                    formatUtcUs(lastQnxUtcUs, qnxStr, sizeof(qnxStr));
                    auto now = std::chrono::steady_clock::now();
                    auto elapsed = std::chrono::duration_cast<std::chrono::milliseconds>(now - lastReport).count();
                    printf("[%lldms] total=%u fd=%u brs=%u seq_gap=%u crc_err=%u sync_err=%u | seq=%u mcu_rel=%u us qnx=%s\n",
                           elapsed, pktCount, fdCount, brsCount, seqGapCount, crcErrCount, syncErrCount,
                           lastSeq, lastMcuRelUs, qnxStr);
                    lastReport = now;
                    lastPrintedCount = pktCount;
                }
                continue;
            }
            printf("recv error (%d)\n", n);
            break;
        }

        int offset = 0;
        while (offset + 12 <= n) {
            if (buf[offset] != 0xAA || buf[offset + 1] != 0x55) {
                syncErrCount++;
                offset++;
                continue;
            }

            uint16_t payloadLen = (uint16_t)(buf[offset + 2] | (buf[offset + 3] << 8));
            int frameLen = 4 + payloadLen + 4;

            if (payloadLen < 30 || offset + frameLen > n) {
                offset++;
                continue;
            }

            uint32_t recvCrc = (uint32_t)buf[offset + 4 + payloadLen]
                             | ((uint32_t)buf[offset + 4 + payloadLen + 1] << 8)
                             | ((uint32_t)buf[offset + 4 + payloadLen + 2] << 16)
                             | ((uint32_t)buf[offset + 4 + payloadLen + 3] << 24);
            uint32_t calcCrc = crc32(&buf[offset + 4], payloadLen);
            if (recvCrc != calcCrc) {
                crcErrCount++;
                offset++;
                continue;
            }

            uint8_t *p = &buf[offset + 4];
            uint32_t seq = (uint32_t)p[0] | ((uint32_t)p[1] << 8)
                         | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);

            if (lastSeq > 0 && seq != lastSeq + 1) {
                seqGapCount++;
                printf("  *** SEQ GAP: expected %u, got %u (missed %d) ***\n",
                       lastSeq + 1, seq, (int)(seq - lastSeq - 1));
            }
            lastSeq = seq;

            uint8_t channel = p[4];
            uint64_t utcUs = (uint64_t)p[5]
                           | ((uint64_t)p[6] << 8)
                           | ((uint64_t)p[7] << 16)
                           | ((uint64_t)p[8] << 24)
                           | ((uint64_t)p[9] << 32)
                           | ((uint64_t)p[10] << 40)
                           | ((uint64_t)p[11] << 48)
                           | ((uint64_t)p[12] << 56);
            uint32_t mcuRelUs = (uint32_t)p[13]
                              | ((uint32_t)p[14] << 8)
                              | ((uint32_t)p[15] << 16)
                              | ((uint32_t)p[16] << 24);
            uint64_t qnxUtcUs = (uint64_t)p[17]
                              | ((uint64_t)p[18] << 8)
                              | ((uint64_t)p[19] << 16)
                              | ((uint64_t)p[20] << 24)
                              | ((uint64_t)p[21] << 32)
                              | ((uint64_t)p[22] << 40)
                              | ((uint64_t)p[23] << 48)
                              | ((uint64_t)p[24] << 56);
            uint32_t canId = ((uint32_t)p[25] << 24) | ((uint32_t)p[26] << 16)
                           | ((uint32_t)p[27] << 8) | p[28];
            uint8_t dlcByte = p[29];
            uint8_t dlc = dlcByte & DLC_MASK;
            (void)canId;
            (void)dlc;
            if (dlcByte & DLC_FLAG_FD) fdCount++;
            if (dlcByte & DLC_FLAG_BRS) brsCount++;

            lastUtcUs = utcUs;
            lastMcuRelUs = mcuRelUs;
            lastQnxUtcUs = qnxUtcUs;
            pktCount++;
            offset += frameLen;
        }

        auto now = std::chrono::steady_clock::now();
        auto elapsed = std::chrono::duration_cast<std::chrono::milliseconds>(now - lastReport).count();
        if (elapsed >= 1000 && pktCount > lastPrintedCount) {
            char qnxStr[64];
            formatUtcUs(lastQnxUtcUs, qnxStr, sizeof(qnxStr));
            printf("[%lldms] total=%u fd=%u brs=%u seq_gap=%u crc_err=%u sync_err=%u | seq=%u mcu_rel=%u us qnx=%s\n",
                   elapsed, pktCount, fdCount, brsCount, seqGapCount, crcErrCount, syncErrCount,
                   lastSeq, lastMcuRelUs, qnxStr);
            lastReport = now;
            lastPrintedCount = pktCount;
        }
    }

    printf("\n=== Final ===\n");
    printf("Total received: %u\n", pktCount);
    printf("CAN-FD frames: %u (BRS: %u)\n", fdCount, brsCount);
    printf("Seq gaps: %u\n", seqGapCount);
    printf("CRC errors: %u\n", crcErrCount);
    printf("Sync errors: %u\n", syncErrCount);
    printf("Last seq: %u\n", lastSeq);

#ifdef _WIN32
    closesocket(sock);
    WSACleanup();
#else
    close(sock);
#endif
    return 0;
}
