//! Shared framed-call contract. Implementations own transport, waiting and identity.
//!
//! A frame is one complete generated protocol document. Implementations enforce
//! one caller budget across connect/send/read, cancellation and frame-byte bounds.
//! Waiting cancellation does not cancel accepted work. An exchange error leaves
//! acceptance uncertain; callers reconcile where their capability supports it.
//! Calls must not be silently retried, rerouted or decoded by the transport.
//! Write completion confirms only the transport's submission semantics.
//! This crate performs no I/O, native linking, discovery or capability dispatch.

pub trait FrameTransport {
    type Error;
    fn write_frame(&self, frame: &[u8]) -> Result<(), Self::Error>;
    fn exchange_frame(&self, frame: &[u8]) -> Result<Vec<u8>, Self::Error>;
}

impl<T: FrameTransport + ?Sized> FrameTransport for &T {
    type Error = T::Error;
    fn write_frame(&self, frame: &[u8]) -> Result<(), Self::Error> {
        T::write_frame(self, frame)
    }
    fn exchange_frame(&self, frame: &[u8]) -> Result<Vec<u8>, Self::Error> {
        T::exchange_frame(self, frame)
    }
}
impl<T: FrameTransport + ?Sized> FrameTransport for std::sync::Arc<T> {
    type Error = T::Error;
    fn write_frame(&self, frame: &[u8]) -> Result<(), Self::Error> {
        self.as_ref().write_frame(frame)
    }
    fn exchange_frame(&self, frame: &[u8]) -> Result<Vec<u8>, Self::Error> {
        self.as_ref().exchange_frame(frame)
    }
}

/// A composite operation shares one waiting budget across all of its frames.
/// Implementations preserve any explicit deadline, cancellation and binding.
/// Fresh default budgets start once here; scoped frames never renew them.
pub trait ScopedTransport: FrameTransport {
    type Scoped: FrameTransport<Error = Self::Error> + Clone;
    fn call_scope(&self) -> Result<Self::Scoped, Self::Error>;
}
