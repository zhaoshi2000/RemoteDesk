#pragma once
#include "transport/wire.hpp"
#include <windows.h>
#include <optional>
#include <string>
namespace rd {
inline std::wstring utf16(std::string_view text){if(text.size()>65536)throw std::runtime_error("clipboard too large");int n=MultiByteToWideChar(CP_UTF8,MB_ERR_INVALID_CHARS,text.data(),int(text.size()),nullptr,0);if(n==0&&!text.empty())throw std::runtime_error("invalid clipboard UTF-8");std::wstring w(n,L'\0');if(n)MultiByteToWideChar(CP_UTF8,MB_ERR_INVALID_CHARS,text.data(),int(text.size()),w.data(),n);return w;}
inline std::string utf8(std::wstring_view text){int n=WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,text.data(),int(text.size()),nullptr,0,nullptr,nullptr);if(n==0&&!text.empty())throw std::runtime_error("invalid clipboard UTF-16");std::string s(n,'\0');if(n)WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,text.data(),int(text.size()),s.data(),n,nullptr,nullptr);return s;}
class TextClipboard {
 HWND owner_;DWORD observed_;std::uint64_t next_=0,lastRemote_=0;std::optional<wire::Message> pending_;
 struct Guard{~Guard(){CloseClipboard();}};
public:
 explicit TextClipboard(HWND owner):owner_(owner),observed_(GetClipboardSequenceNumber()){}
 void receive(wire::Message m){if(m.kind!=wire::clipboard_text||m.payload.size()>65536||m.id<=lastRemote_)return;pending_=std::move(m);apply();}
 void apply(){if(!pending_||!OpenClipboard(owner_))return;Guard g;auto text=utf16({reinterpret_cast<const char*>(pending_->payload.data()),pending_->payload.size()});if(text.find(L'\0')!=std::wstring::npos){pending_.reset();return;}auto size=(text.size()+1)*sizeof(wchar_t);HGLOBAL memory=GlobalAlloc(GMEM_MOVEABLE,size);if(!memory)return;void* p=GlobalLock(memory);if(!p){GlobalFree(memory);return;}std::memcpy(p,text.c_str(),size);GlobalUnlock(memory);if(!EmptyClipboard()||!SetClipboardData(CF_UNICODETEXT,memory)){GlobalFree(memory);return;}observed_=GetClipboardSequenceNumber();lastRemote_=pending_->id;pending_.reset();}
 std::optional<wire::Message> poll(){apply();DWORD n=GetClipboardSequenceNumber();if(n==observed_)return {};if(!OpenClipboard(owner_))return {};Guard g;observed_=n;if(!IsClipboardFormatAvailable(CF_UNICODETEXT))return {};HANDLE h=GetClipboardData(CF_UNICODETEXT);if(!h)return {};SIZE_T bytes=GlobalSize(h);if(bytes>131074||bytes<2)return {};const auto* p=static_cast<const wchar_t*>(GlobalLock(h));if(!p)return {};const auto max=bytes/sizeof(wchar_t);std::size_t len=0;while(len<max&&p[len])++len;std::wstring copy(p,len);GlobalUnlock(h);if(len==max)return {};auto text=utf8(copy);if(text.size()>65536)return {};wire::Message m;m.channel=wire::clipboard;m.kind=wire::clipboard_text;m.id=++next_;m.payload.assign(text.begin(),text.end());return m;}
};
}
