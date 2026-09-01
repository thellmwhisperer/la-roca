#ifndef ROCA_VECTOR_LLAMA_WRAPPER_H
#define ROCA_VECTOR_LLAMA_WRAPPER_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct roca_llama_engine roca_llama_engine;

roca_llama_engine * roca_llama_open(const char * model_path, int threads, int gpu_layers,
                                    int * accelerated, char ** error);
int roca_llama_embed(roca_llama_engine * engine, const char * text, size_t text_size, float ** embedding,
                     int * dimensions, int * token_count, char ** error);
void roca_llama_close(roca_llama_engine * engine);
void roca_llama_release(void * memory);
void roca_llama_request_abort(void);
void roca_llama_clear_abort(void);

#ifdef __cplusplus
}
#endif

#endif
