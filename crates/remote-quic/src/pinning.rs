//! Device certificates are deliberately self-signed. The public key is pinned by
//! the separately authenticated, local device ACL; DNS/WebPKI is not the authority.
use std::sync::Arc;
use rustls::{
    client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier},
    server::danger::{ClientCertVerified, ClientCertVerifier},
    pki_types::{CertificateDer, ServerName, UnixTime},
    DigitallySignedStruct, DistinguishedName, Error, SignatureScheme,
};
use x509_parser::{parse_x509_certificate, time::ASN1Time};

#[derive(Debug)]
pub struct Pin {
    key: [u8; 32],
    provider: Arc<rustls::crypto::CryptoProvider>,
    hints: Vec<DistinguishedName>,
}
impl Pin {
    pub fn new(key: [u8; 32]) -> Self {
        Self { key, provider: Arc::new(rustls::crypto::ring::default_provider()), hints: vec![] }
    }
    fn certificate(&self, der: &CertificateDer<'_>, chain: &[CertificateDer<'_>], now: UnixTime) -> Result<(), Error> {
        if !chain.is_empty() || der.len() > 16384 {
            return Err(Error::General("only one device certificate is permitted".into()));
        }
        let (tail, cert) = parse_x509_certificate(der.as_ref())
            .map_err(|_| Error::General("malformed device certificate".into()))?;
        if !tail.is_empty() || cert.public_key().algorithm.algorithm.to_id_string() != "1.3.101.112" {
            return Err(Error::General("device certificate is not Ed25519".into()));
        }
        let raw = &cert.public_key().subject_public_key.data;
        // Public pins are not secrets; equality is followed by cryptographic proof of possession.
        if raw.as_ref() != self.key.as_slice() || cert.public_key().subject_public_key.unused_bits != 0 {
            return Err(Error::General("device public key does not match local authorization".into()));
        }
        let at = ASN1Time::from_timestamp(now.as_secs().try_into().map_err(|_| Error::General("invalid clock".into()))?)
            .map_err(|_| Error::General("invalid clock".into()))?;
        if !cert.validity().is_valid_at(at) || cert.issuer() != cert.subject() {
            return Err(Error::General("expired or non-self-issued device certificate".into()));
        }
        cert.verify_signature(None).map_err(|_| Error::General("invalid device certificate signature".into()))
    }
    fn signature(&self, message: &[u8], cert: &CertificateDer<'_>, sig: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, Error> {
        if sig.scheme != SignatureScheme::ED25519 {
            return Err(Error::General("device handshake requires Ed25519".into()));
        }
        rustls::crypto::verify_tls13_signature(message, cert, sig, &self.provider.signature_verification_algorithms)
    }
}
impl ServerCertVerifier for Pin {
    fn verify_server_cert(&self, end: &CertificateDer<'_>, intermediates: &[CertificateDer<'_>], _name: &ServerName<'_>, _ocsp: &[u8], now: UnixTime) -> Result<ServerCertVerified, Error> {
        self.certificate(end, intermediates, now)?;
        Ok(ServerCertVerified::assertion())
    }
    fn verify_tls12_signature(&self, _m: &[u8], _c: &CertificateDer<'_>, _s: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, Error> {
        Err(Error::General("TLS 1.2 is disabled".into()))
    }
    fn verify_tls13_signature(&self, m: &[u8], c: &CertificateDer<'_>, s: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, Error> { self.signature(m,c,s) }
    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> { vec![SignatureScheme::ED25519] }
}
impl ClientCertVerifier for Pin {
    fn offer_client_auth(&self) -> bool { true }
    fn client_auth_mandatory(&self) -> bool { true }
    fn root_hint_subjects(&self) -> &[DistinguishedName] { &self.hints }
    fn verify_client_cert(&self, end: &CertificateDer<'_>, intermediates: &[CertificateDer<'_>], now: UnixTime) -> Result<ClientCertVerified, Error> {
        self.certificate(end, intermediates, now)?;
        Ok(ClientCertVerified::assertion())
    }
    fn verify_tls12_signature(&self, _m: &[u8], _c: &CertificateDer<'_>, _s: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, Error> {
        Err(Error::General("TLS 1.2 is disabled".into()))
    }
    fn verify_tls13_signature(&self, m: &[u8], c: &CertificateDer<'_>, s: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, Error> { self.signature(m,c,s) }
    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> { vec![SignatureScheme::ED25519] }
}
