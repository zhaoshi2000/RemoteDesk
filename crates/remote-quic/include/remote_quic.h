#pragma once
#include <stddef.h>
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
/* Calls require valid pointers. Never destroy a handle concurrently with another call. */
typedef struct RdqSession RdqSession;
typedef struct RdqConfig { uint8_t server; uint16_t peer_port; const uint8_t* certificate; size_t certificate_len; const uint8_t* private_key; size_t private_key_len; uint8_t peer_key[32]; } RdqConfig;
RdqSession* rdq_create(const RdqConfig*,char* error,size_t capacity);
void rdq_destroy(RdqSession*);
uint16_t rdq_port(const RdqSession*);
/* 0 connecting, 1 authenticated, 2 closed, -1 failed */
int32_t rdq_state(const RdqSession*);
void rdq_error(const RdqSession*,char* error,size_t capacity);
int32_t rdq_send(const RdqSession*,const uint8_t*,size_t);
intptr_t rdq_receive(const RdqSession*,uint8_t*,size_t);
void rdq_acknowledge(const RdqSession*,uint64_t);
size_t rdq_queued_video(const RdqSession*);
uint64_t rdq_dropped(const RdqSession*);
uint64_t rdq_rtt_us(const RdqSession*);
#ifdef __cplusplus
}
#endif
