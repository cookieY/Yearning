from Crypto.Cipher import AES
from binascii import b2a_hex, a2b_hex


class cryptoAES(object):
    def __init__(self, key):
        self.key = key[0:16]
        self.mode = AES.MODE_CBC
        self.ciphertext = None

    def encrypt(self, text):
        cryptor = AES.new(self.key, self.mode, self.key)
        length = 16
        count = len(text)
        if count % length != 0:
            add = length - (count % length)
        else:
            add = 0
        text = text + ('\0' * add)
        self.ciphertext = cryptor.encrypt(text)
        return bytes.decode(b2a_hex(self.ciphertext))

    def decrypt(self, text):
        cryptor = AES.new(self.key, self.mode, self.key)
        plain_text = cryptor.decrypt(a2b_hex(text))
        return bytes.decode(plain_text.rstrip(b'\0'))