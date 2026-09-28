#pragma once
#include <array>
#include <cstdint>
#include <string>
namespace rd {
struct QuicConfig { bool server=false;std::string certificate_pem,private_key_pem;std::array<unsigned char,32> peer_key{};std::uint16_t peer_port=0; };
}
