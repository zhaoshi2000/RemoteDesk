#pragma once
// Platform-independent media scheduling and adaptive rate decisions.
#include <algorithm>
#include <array>
#include <cmath>
#include <cstdint>
#include <deque>
#include <mutex>
#include <optional>
#include <stdexcept>
#include <utility>
#include <vector>

namespace rd {
enum class Channel : std::uint8_t { control=0, mouse_position=1, keyboard=2, video=3, audio=4, clipboard=5, ssh=6, file=7 };
struct Packet { Channel channel; std::vector<std::uint8_t> data; std::uint64_t deadline_us=0; };
class Scheduler {
    std::array<std::deque<Packet>,8> queues_;
    std::size_t bytes_=0, maximum_;
    mutable std::mutex mutex_;
public:
    explicit Scheduler(std::size_t max_bytes=4*1024*1024):maximum_(max_bytes){}
    bool push(Packet packet) {
        const auto index=static_cast<std::size_t>(packet.channel);
        if(index>=queues_.size() || packet.data.size()>maximum_) return false;
        std::lock_guard lock(mutex_);
        auto& queue=queues_[index];
        // Only absolute pointer POSITION is coalesced. Buttons belong on reliable input/control.
        if(packet.channel==Channel::mouse_position) {
            for(const auto& old:queue) bytes_-=old.data.size();
            queue.clear();
        }
        const bool urgent=packet.channel==Channel::control||packet.channel==Channel::keyboard||packet.channel==Channel::mouse_position||packet.channel==Channel::ssh||packet.channel==Channel::audio;
        const auto reserved=std::min<std::size_t>(64*1024,maximum_/4);
        if(!urgent && bytes_+packet.data.size()>maximum_-reserved) return false;
        if(urgent && bytes_+packet.data.size()>maximum_) {
            for(const auto low:std::array<std::size_t,1>{3}){
                while(!queues_[low].empty() && bytes_+packet.data.size()>maximum_){bytes_-=queues_[low].front().data.size();queues_[low].pop_front();}
            }
        }
        if(bytes_+packet.data.size()>maximum_) return false;
        if(queue.size()>=256) return false;
        bytes_+=packet.data.size();queue.push_back(std::move(packet));return true;
    }
    std::optional<Packet> pop(std::uint64_t now_us) {
        std::lock_guard lock(mutex_);
        constexpr std::array<std::size_t,8> priority={0,2,1,6,4,5,3,7};
        for(const auto index:priority) {
            auto& queue=queues_[index];
            while(!queue.empty() && queue.front().deadline_us!=0 && queue.front().deadline_us<=now_us){bytes_-=queue.front().data.size();queue.pop_front();}
            if(queue.empty()) continue;
            auto p=std::move(queue.front());queue.pop_front();bytes_-=p.data.size();return p;
        }
        return std::nullopt;
    }
    std::size_t bytes() const {std::lock_guard lock(mutex_);return bytes_;}
};
struct NetworkSample {double rtt_ms=0, queue_ms=0, loss=0;std::uint64_t now_ms=0;};
struct VideoTarget {std::uint32_t bitrate_bps=12'000'000, fps=60, width=1920, height=1080;};
class AdaptiveRate {
    VideoTarget target_;
    VideoTarget ceiling_;
    double baseline_rtt_=0;
    std::uint64_t changed_ms_=0, healthy_since_=0, last_sample_=0;
    bool initialized_=false;
public:
    explicit AdaptiveRate(VideoTarget maximum={}):target_(maximum),ceiling_(maximum){}
    VideoTarget target()const{return target_;}
    VideoTarget update(NetworkSample s){
        if(!std::isfinite(s.rtt_ms)||!std::isfinite(s.queue_ms)||!std::isfinite(s.loss)||s.rtt_ms<0||s.queue_ms<0||s.loss<0||s.loss>1)throw std::invalid_argument("invalid network sample");
        if(initialized_&&s.now_ms<last_sample_)throw std::invalid_argument("monotonic sample time required");
        last_sample_=s.now_ms;
        if(!initialized_){initialized_=true;baseline_rtt_=s.rtt_ms;changed_ms_=healthy_since_=s.now_ms;return target_;}
        baseline_rtt_=std::min(baseline_rtt_,s.rtt_ms);
        const bool congested=s.loss>0.03||s.queue_ms>25||s.rtt_ms-baseline_rtt_>35;
        if(congested){healthy_since_=s.now_ms;if(s.now_ms-changed_ms_>=500){target_.bitrate_bps=std::max<std::uint32_t>(750'000,static_cast<std::uint32_t>(target_.bitrate_bps*0.82));changed_ms_=s.now_ms;}}
        else if(s.now_ms-healthy_since_>=2000 && s.now_ms-changed_ms_>=2000){target_.bitrate_bps=std::min<std::uint32_t>(ceiling_.bitrate_bps,static_cast<std::uint32_t>(target_.bitrate_bps*1.06)+50'000);changed_ms_=s.now_ms;}
        // Coarse operating points; actual encoding resolution changes require an IDR and negotiation.
        if(target_.bitrate_bps<1'800'000){target_.width=1280;target_.height=720;target_.fps=30;}
        else if(target_.bitrate_bps<3'500'000){target_.width=1600;target_.height=900;target_.fps=30;}
        else if(target_.bitrate_bps<6'000'000){target_.width=1920;target_.height=1080;target_.fps=45;}
        else{target_.width=ceiling_.width;target_.height=ceiling_.height;target_.fps=ceiling_.fps;}
        return target_;
    }
};
} // namespace rd
