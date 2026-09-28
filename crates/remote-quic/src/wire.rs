//! RDV2 framing, shared by the C++ and Rust transports.
pub const HEADER: usize = 32;
pub const VIDEO: u8 = 3;
pub const MAX_VIDEO: usize = 4 * 1024 * 1024;
pub const MAX_CONTROL: usize = 128 * 1024;
pub const MAX_QUEUED: usize = 8 * 1024 * 1024;
#[derive(Clone, Copy, Debug)]
pub struct Header { pub channel: u8, pub id: u64, pub size: usize }
pub fn header(b: &[u8]) -> Result<Header, &'static str> {
    if b.len() < HEADER || &b[..4] != b"RDV2" { return Err("invalid RDV2 magic/length"); }
    if ![0,1,3,4,5,7].contains(&b[4]) || u16::from_be_bytes([b[6],b[7]]) > 3 || b[28..32] != [0;4] {
        return Err("invalid RDV2 channel/flags/reserved");
    }
    let size = u32::from_be_bytes(b[24..28].try_into().unwrap()) as usize;
    if size > if b[4] == VIDEO { MAX_VIDEO } else { MAX_CONTROL } { return Err("RDV2 payload exceeds limit"); }
    Ok(Header { channel: b[4], id: u64::from_be_bytes(b[8..16].try_into().unwrap()), size })
}
pub fn frame(b: &[u8]) -> Result<Header, &'static str> {
    let h = header(b)?;
    if b.len() != HEADER + h.size { return Err("RDV2 payload length mismatch"); }
    Ok(h)
}
pub fn priority(c: u8) -> i32 { match c { 0|1 => 100, 4 => 80, 5 => 60, 3 => 20, _ => 0 } }
#[cfg(test)]
mod tests {
    use super::*;
    #[test] fn bounds_and_reserved() {
        let mut b = [0u8; HEADER]; b[..4].copy_from_slice(b"RDV2"); b[4] = 3;
        assert!(frame(&b).is_ok()); b[31]=1; assert!(frame(&b).is_err()); b[31]=0;
        b[24..28].copy_from_slice(&(MAX_VIDEO as u32 + 1).to_be_bytes()); assert!(header(&b).is_err());
        b[24..28].copy_from_slice(&0u32.to_be_bytes()); b[4]=2; assert!(frame(&b).is_err());
        assert!(header(&b[..31]).is_err());
    }
}
