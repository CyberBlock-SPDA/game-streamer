// FFmpeg's CLI captures X11 and converts to yuv420p. Using libavcodec here
// lets the agent force an IDR on RTCP feedback without restarting capture.
#define _POSIX_C_SOURCE 200809L
#include <arpa/inet.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>

static volatile sig_atomic_t request_idr;
static void keyframe(int signal_number) {
    (void)signal_number;
    request_idr = 1;
}

static void check(int result, const char *operation) {
    if (result < 0) {
        char message[AV_ERROR_MAX_STRING_SIZE];
        av_strerror(result, message, sizeof message);
        fprintf(stderr, "%s: %s\n", operation, message);
        exit(1);
    }
}

int main(int argc, char **argv) {
    if (argc != 5) return 2;
    int width = atoi(argv[1]), height = atoi(argv[2]), fps = atoi(argv[3]);
    const AVCodec *codec = avcodec_find_encoder_by_name("libx264");
    if (!codec) return 1;
    AVCodecContext *enc = avcodec_alloc_context3(codec);
    AVFrame *frame = av_frame_alloc();
    AVPacket *packet = av_packet_alloc();
    if (!enc || !frame || !packet) return 1;

    enc->width = frame->width = width;
    enc->height = frame->height = height;
    enc->pix_fmt = frame->format = AV_PIX_FMT_YUV420P;
    enc->time_base = (AVRational){1, fps};
    enc->framerate = (AVRational){fps, 1};
    enc->max_b_frames = 0;
    enc->gop_size = fps;
    enc->thread_count = 1;
    check(av_opt_set(enc, "b", argv[4], 0), "bitrate");
    enc->rc_max_rate = enc->bit_rate;
    enc->rc_buffer_size = enc->bit_rate / 2;
    check(av_opt_set(enc->priv_data, "preset", "ultrafast", 0), "preset");
    check(av_opt_set(enc->priv_data, "tune", "zerolatency", 0), "tune");
    check(av_opt_set(enc->priv_data, "profile", "baseline", 0), "profile");
    check(av_opt_set(enc->priv_data, "level", "3.1", 0), "level");
    check(av_opt_set(enc->priv_data, "forced-idr", "1", 0), "forced-idr");
    check(av_opt_set(enc->priv_data, "x264-params",
                     "aud=1:repeat-headers=1:scenecut=0", 0), "x264-params");
    check(avcodec_open2(enc, codec, NULL), "open encoder");
    check(av_frame_get_buffer(frame, 32), "allocate frame");
    struct sigaction action = {.sa_handler = keyframe, .sa_flags = SA_RESTART};
    sigemptyset(&action.sa_mask);
    if (sigaction(SIGUSR1, &action, NULL) != 0) return 1;

    for (int64_t pts = 0; ; pts++) {
        check(av_frame_make_writable(frame), "prepare frame");
        for (int plane = 0; plane < 3; plane++) {
            int columns = plane ? width / 2 : width;
            int rows = plane ? height / 2 : height;
            for (int y = 0; y < rows; y++) {
                uint8_t *row = frame->data[plane] + y * frame->linesize[plane];
                if (fread(row, 1, columns, stdin) != (size_t)columns) {
                    if (feof(stdin) && !plane && !y) goto done;
                    return 1;
                }
            }
        }
        frame->pts = pts;
        frame->pict_type = AV_PICTURE_TYPE_NONE;
        if (request_idr) {
            request_idr = 0;
            frame->pict_type = AV_PICTURE_TYPE_I;
        }
        check(avcodec_send_frame(enc, frame), "encode frame");
        int result;
        while ((result = avcodec_receive_packet(enc, packet)) >= 0) {
            // One video packet is a complete Annex B access unit, including
            // every slice and any AUD/SPS/PPS. Preserve that boundary on stdout.
            uint32_t size = htonl((uint32_t)packet->size);
            if (fwrite(&size, sizeof size, 1, stdout) != 1 ||
                fwrite(packet->data, 1, packet->size, stdout) != (size_t)packet->size ||
                fflush(stdout) != 0) return 1;
            av_packet_unref(packet);
        }
        if (result != AVERROR(EAGAIN)) check(result, "receive packet");
    }
done:
    av_packet_free(&packet);
    av_frame_free(&frame);
    avcodec_free_context(&enc);
    return 0;
}
