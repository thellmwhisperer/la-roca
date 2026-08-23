#ifndef ROCA_VECTOR_LLAMA_WRAPPER_H
#define ROCA_VECTOR_LLAMA_WRAPPER_H

#ifdef __cplusplus
extern "C" {
#endif

typedef struct roca_llama_engine roca_llama_engine;

roca_llama_engine * roca_llama_open(const char * model_path, int threads, int gpu_layers, char ** error);
int roca_llama_embed(roca_llama_engine * engine, const char * text, float ** embedding,
                     int * dimensions, int * token_count, char ** error);
void roca_llama_close(roca_llama_engine * engine);
void roca_llama_release(void * memory);

#ifdef __cplusplus
}
#endif

#endif
