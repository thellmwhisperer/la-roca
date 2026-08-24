#include "wrapper.h"

#include "llama.h"
#include "llama-ext.h"

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

struct roca_llama_engine {
    llama_model * model;
    llama_context * context;
};

static void quiet_log(enum ggml_log_level, const char *, void *) {}

static void fail(char ** error, const std::string & message) {
    if (error != nullptr) {
        *error = strdup(message.c_str());
    }
}

extern "C" roca_llama_engine * roca_llama_open(const char * model_path, int threads, int gpu_layers,
                                                 int * accelerated, char ** error) {
    llama_log_set(quiet_log, nullptr);
    llama_backend_init();

    llama_model_params model_params = llama_model_default_params();
    model_params.n_gpu_layers = gpu_layers;
    llama_model * model = llama_model_load_from_file(model_path, model_params);
    if (model == nullptr) {
        fail(error, "model load failed");
        return nullptr;
    }

    if (accelerated != nullptr) {
        *accelerated = 0;
        if (gpu_layers != 0) {
            const int32_t devices = llama_model_n_devices(model);
            for (int32_t i = 0; i < devices; ++i) {
                const enum ggml_backend_dev_type type = ggml_backend_dev_type(llama_model_get_device(model, i));
                if (type == GGML_BACKEND_DEVICE_TYPE_GPU || type == GGML_BACKEND_DEVICE_TYPE_ACCEL) {
                    *accelerated = 1;
                    break;
                }
            }
        }
    }

    llama_context_params context_params = llama_context_default_params();
    context_params.n_ctx = 512;
    context_params.n_batch = 512;
    context_params.n_ubatch = 512;
    context_params.n_seq_max = 1;
    context_params.n_threads = threads;
    context_params.n_threads_batch = threads;
    context_params.embeddings = true;
    context_params.pooling_type = LLAMA_POOLING_TYPE_MEAN;
    context_params.attention_type = LLAMA_ATTENTION_TYPE_NON_CAUSAL;
    context_params.no_perf = true;
    llama_context * context = llama_init_from_model(model, context_params);
    if (context == nullptr) {
        llama_model_free(model);
        fail(error, "context init failed");
        return nullptr;
    }
    return new roca_llama_engine{model, context};
}

extern "C" int roca_llama_embed(
    roca_llama_engine * engine,
    const char * text,
    size_t text_size,
    float ** embedding,
    int * dimensions,
    int * token_count,
    char ** error) {
    if (engine == nullptr || text == nullptr || embedding == nullptr || dimensions == nullptr || token_count == nullptr) {
        fail(error, "invalid embed arguments");
        return 1;
    }

    if (text_size > static_cast<size_t>(INT32_MAX)) {
        fail(error, "input length overflow");
        return 1;
    }
    const llama_vocab * vocab = llama_model_get_vocab(engine->model);
    const int32_t text_length = static_cast<int32_t>(text_size);
    int32_t wanted = llama_tokenize(vocab, text, text_length, nullptr, 0, true, true);
    if (wanted == INT32_MIN) {
        fail(error, "token count overflow");
        return 1;
    }
    wanted = std::abs(wanted);
    std::vector<llama_token> tokens(static_cast<size_t>(wanted));
    int32_t count = llama_tokenize(vocab, text, text_length, tokens.data(), wanted, true, true);
    if (count < 0) {
        fail(error, "tokenization failed");
        return 1;
    }
    tokens.resize(static_cast<size_t>(count));

    // Ollama /api/embed truncates at the model context. This model spends two
    // of the 512 positions on BOS and EOS; keep the head and restore EOS.
    if (tokens.size() > 512) {
        tokens.resize(512);
        tokens.back() = llama_vocab_eos(vocab);
    }
    if (tokens.empty()) {
        fail(error, "tokenization returned no tokens");
        return 1;
    }

    llama_batch batch = llama_batch_init(static_cast<int32_t>(tokens.size()), 0, 1);
    batch.n_tokens = static_cast<int32_t>(tokens.size());
    for (int32_t i = 0; i < batch.n_tokens; ++i) {
        batch.token[i] = tokens[static_cast<size_t>(i)];
        batch.pos[i] = i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = i == batch.n_tokens - 1 ? 1 : 0;
    }

    llama_memory_clear(llama_get_memory(engine->context), true);
    const int32_t decoded = llama_decode(engine->context, batch);
    llama_batch_free(batch);
    if (decoded != 0) {
        fail(error, "decode failed");
        return 1;
    }

    const int n = llama_model_n_embd_out(engine->model);
    const float * raw = llama_get_embeddings_seq(engine->context, 0);
    if (raw == nullptr || n <= 0) {
        fail(error, "no embedding");
        return 1;
    }
    float * result = static_cast<float *>(std::malloc(sizeof(float) * static_cast<size_t>(n)));
    if (result == nullptr) {
        fail(error, "embedding allocation failed");
        return 1;
    }
    double sum = 0;
    for (int i = 0; i < n; ++i) {
        sum += static_cast<double>(raw[i]) * static_cast<double>(raw[i]);
    }
    const float scale = sum > 0 ? static_cast<float>(1.0 / std::sqrt(sum)) : 0;
    for (int i = 0; i < n; ++i) {
        result[i] = raw[i] * scale;
    }
    *embedding = result;
    *dimensions = n;
    *token_count = static_cast<int>(tokens.size());
    return 0;
}

extern "C" void roca_llama_close(roca_llama_engine * engine) {
    if (engine == nullptr) {
        return;
    }
    llama_free(engine->context);
    llama_model_free(engine->model);
    delete engine;
}

extern "C" void roca_llama_release(void * memory) {
    std::free(memory);
}
