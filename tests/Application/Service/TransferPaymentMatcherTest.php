<?php

declare(strict_types=1);

namespace App\Tests\Application\Service;

use App\Application\Service\TransferPaymentMatcher;
use App\Entity\ClassCouncil\ClassRoom;
use App\Entity\ClassCouncil\Student;
use App\Entity\ClassCouncil\StudentPayment;
use App\Entity\Payment;
use App\Entity\Transfer;
use App\Tests\Assembler\PaymentAssembler;
use App\Tests\Assembler\PaymentCodeAssembler;
use App\Tests\Assembler\TransferAssembler;
use App\Tests\Assembler\UserAssembler;
use Brick\Money\Money;
use Doctrine\ORM\EntityManagerInterface;
use PHPUnit\Framework\Attributes\Group;
use PHPUnit\Framework\Attributes\Test;
use Symfony\Bundle\FrameworkBundle\Test\KernelTestCase;

#[Group('functional')]
final class TransferPaymentMatcherTest extends KernelTestCase
{
    private EntityManagerInterface $entityManager;

    private TransferPaymentMatcher $matcher;

    protected function setUp(): void
    {
        self::bootKernel();

        $this->entityManager = self::getContainer()->get(EntityManagerInterface::class);
        $this->matcher = self::getContainer()->get(TransferPaymentMatcher::class);
    }

    #[Test]
    public function ranksPaymentsWithHistoricalStudentMatchFirst(): void
    {
        $parent = UserAssembler::new()
            ->withName('Anna Kowalska')
            ->withEmail('anna@example.com')
            ->assemble();
        $this->entityManager->persist($parent);

        $classRoom = new ClassRoom('4A');
        $this->entityManager->persist($classRoom);

        $student = new Student($classRoom, 'Jan', 'Kowalski');
        $student->addParent($parent);
        $this->entityManager->persist($student);

        $historicalPayment = PaymentAssembler::new()
            ->withUser($parent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PAID)
            ->assemble();
        $this->entityManager->persist($historicalPayment);

        $historicalStudentPayment = new StudentPayment($student, 'Wycieczka', Money::of('100.00', 'PLN'));
        $historicalStudentPayment->setPayment($historicalPayment);
        $historicalStudentPayment->setStatus(StudentPayment::STATUS_PAID);
        $this->entityManager->persist($historicalStudentPayment);

        $historicalTransfer = TransferAssembler::new()
            ->withAccountNumber('PL61109010140000071219812874')
            ->withSender('Anna Kowalska')
            ->withTitle('Wycieczka')
            ->withAmount('100.00')
            ->assemble();
        $historicalTransfer->setPayment($historicalPayment);
        $this->entityManager->persist($historicalTransfer);

        $otherParent = UserAssembler::new()
            ->withName('Inny Rodzic')
            ->withEmail('inny@example.com')
            ->assemble();
        $this->entityManager->persist($otherParent);

        $otherPayment = PaymentAssembler::new()
            ->withUser($otherParent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PENDING)
            ->assemble();
        $this->entityManager->persist($otherPayment);

        $matchedPayment = PaymentAssembler::new()
            ->withUser($parent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PENDING)
            ->assemble();
        $this->entityManager->persist($matchedPayment);

        $matchedStudentPayment = new StudentPayment($student, 'Obiad', Money::of('100.00', 'PLN'));
        $matchedStudentPayment->setPayment($matchedPayment);
        $this->entityManager->persist($matchedStudentPayment);

        $incomingTransfer = TransferAssembler::new()
            ->withAccountNumber('PL61109010140000071219812874')
            ->withSender('Anna Kowalska')
            ->withTitle('Brak kodu')
            ->withAmount('100.00')
            ->assemble();
        $this->entityManager->persist($incomingTransfer);
        $this->entityManager->flush();

        $enriched = $this->matcher->enrichAssignablePayments($incomingTransfer, [$otherPayment, $matchedPayment]);

        self::assertSame($matchedPayment->getId(), $enriched['payments'][0]->getId());
        self::assertArrayHasKey($matchedPayment->getId()->toRfc4122(), $enriched['hints']);
        self::assertStringContainsString(
            'Jan Kowalski',
            $enriched['hints'][$matchedPayment->getId()->toRfc4122()]->getDescription()
        );
    }

    #[Test]
    public function matchesIncomingTransferByHistoricalStudentDataWhenAmountMatches(): void
    {
        $parent = UserAssembler::new()
            ->withName('Anna Kowalska')
            ->withEmail('anna@example.com')
            ->assemble();
        $this->entityManager->persist($parent);

        $classRoom = new ClassRoom('4A');
        $this->entityManager->persist($classRoom);

        $student = new Student($classRoom, 'Jan', 'Kowalski');
        $student->addParent($parent);
        $this->entityManager->persist($student);

        $historicalPayment = PaymentAssembler::new()
            ->withUser($parent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PAID)
            ->assemble();
        $this->entityManager->persist($historicalPayment);

        $historicalStudentPayment = new StudentPayment($student, 'Wycieczka', Money::of('100.00', 'PLN'));
        $historicalStudentPayment->setPayment($historicalPayment);
        $historicalStudentPayment->setStatus(StudentPayment::STATUS_PAID);
        $this->entityManager->persist($historicalStudentPayment);

        $historicalTransfer = TransferAssembler::new()
            ->withAccountNumber('PL61109010140000071219812874')
            ->withSender('Anna Kowalska')
            ->withTitle('Wycieczka')
            ->withAmount('100.00')
            ->assemble();
        $historicalTransfer->setPayment($historicalPayment);
        $this->entityManager->persist($historicalTransfer);

        $pendingPayment = PaymentAssembler::new()
            ->withUser($parent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PENDING)
            ->assemble();
        $this->entityManager->persist($pendingPayment);

        $pendingStudentPayment = new StudentPayment($student, 'Obiad', Money::of('100.00', 'PLN'));
        $pendingStudentPayment->setPayment($pendingPayment);
        $this->entityManager->persist($pendingStudentPayment);

        $incomingTransfer = TransferAssembler::new()
            ->withAccountNumber('PL61109010140000071219812874')
            ->withSender('Anna Kowalska')
            ->withTitle('Brak kodu')
            ->withAmount('100.00')
            ->assemble();
        $this->entityManager->persist($incomingTransfer);
        $this->entityManager->flush();

        $matchedPayment = $this->matcher->matchByHistoricalData($incomingTransfer);

        self::assertNotNull($matchedPayment);
        self::assertTrue($pendingPayment->getId()->equals($matchedPayment->getId()));
    }

    #[Test]
    public function doesNotAutoMatchWhenMultipleHistoricalCandidatesExist(): void
    {
        $parent = UserAssembler::new()
            ->withName('Anna Kowalska')
            ->withEmail('anna@example.com')
            ->assemble();
        $this->entityManager->persist($parent);

        $classRoom = new ClassRoom('4A');
        $this->entityManager->persist($classRoom);

        $student = new Student($classRoom, 'Jan', 'Kowalski');
        $student->addParent($parent);
        $this->entityManager->persist($student);

        $historicalPayment = PaymentAssembler::new()
            ->withUser($parent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PAID)
            ->assemble();
        $this->entityManager->persist($historicalPayment);

        $historicalStudentPayment = new StudentPayment($student, 'Wycieczka', Money::of('100.00', 'PLN'));
        $historicalStudentPayment->setPayment($historicalPayment);
        $historicalStudentPayment->setStatus(StudentPayment::STATUS_PAID);
        $this->entityManager->persist($historicalStudentPayment);

        $historicalTransfer = TransferAssembler::new()
            ->withAccountNumber('PL61109010140000071219812874')
            ->withSender('Anna Kowalska')
            ->withAmount('100.00')
            ->assemble();
        $historicalTransfer->setPayment($historicalPayment);
        $this->entityManager->persist($historicalTransfer);

        $firstPending = PaymentAssembler::new()
            ->withUser($parent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PENDING)
            ->assemble();
        $this->entityManager->persist($firstPending);

        $firstStudentPayment = new StudentPayment($student, 'Obiad', Money::of('100.00', 'PLN'));
        $firstStudentPayment->setPayment($firstPending);
        $this->entityManager->persist($firstStudentPayment);

        $secondPending = PaymentAssembler::new()
            ->withUser($parent)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PENDING)
            ->assemble();
        $this->entityManager->persist($secondPending);

        $secondStudentPayment = new StudentPayment($student, 'Książki', Money::of('100.00', 'PLN'));
        $secondStudentPayment->setPayment($secondPending);
        $this->entityManager->persist($secondStudentPayment);

        $incomingTransfer = TransferAssembler::new()
            ->withAccountNumber('PL61109010140000071219812874')
            ->withSender('Anna Kowalska')
            ->withAmount('100.00')
            ->assemble();
        $this->entityManager->persist($incomingTransfer);
        $this->entityManager->flush();

        self::assertNull($this->matcher->matchByHistoricalData($incomingTransfer));
    }

    #[Test]
    public function matchesPaymentByCodeFoundInTransferTitle(): void
    {
        $payment = $this->persistPaymentWithCode('AB12');
        $this->entityManager->flush();

        self::assertSame(
            $payment,
            $this->matcher->matchByPaymentCode($this->transferTitled('Wpłata AB12 za wycieczkę'))
        );
    }

    #[Test]
    public function matchesPaymentByCodeSplitAcrossWords(): void
    {
        $payment = $this->persistPaymentWithCode('AB12');
        $this->entityManager->flush();

        self::assertSame($payment, $this->matcher->matchByPaymentCode($this->transferTitled('kod AB 12')));
    }

    #[Test]
    public function matchesPaymentByCodeWithZeroAndLetterOConfused(): void
    {
        $zeroCode = $this->persistPaymentWithCode('A0B1');
        $letterCode = $this->persistPaymentWithCode('XYZO');
        $this->entityManager->flush();

        self::assertSame($zeroCode, $this->matcher->matchByPaymentCode($this->transferTitled('AOB1')));
        self::assertSame($letterCode, $this->matcher->matchByPaymentCode($this->transferTitled('XYZ0')));
    }

    #[Test]
    public function keepsTokenOrderWhenSeveralCandidatesAreKnownCodes(): void
    {
        $first = $this->persistPaymentWithCode('AAAA');
        $second = $this->persistPaymentWithCode('BBBB');
        $this->entityManager->flush();

        self::assertSame($first, $this->matcher->matchByPaymentCode($this->transferTitled('AAAA BBBB')));
        self::assertSame($second, $this->matcher->matchByPaymentCode($this->transferTitled('BBBB AAAA')));
    }

    #[Test]
    public function prefersSingleWordCandidatesOverWordsGluedTogether(): void
    {
        $glued = $this->persistPaymentWithCode('ABCD');
        $single = $this->persistPaymentWithCode('EFGH');
        $this->entityManager->flush();

        // "AB" + "CD" form a known code only when glued, "EFGH" is a code on its own and is tried first
        self::assertSame($single, $this->matcher->matchByPaymentCode($this->transferTitled('AB CD EFGH')));
        self::assertSame($glued, $this->matcher->matchByPaymentCode($this->transferTitled('AB CD')));
    }

    #[Test]
    public function doesNotMatchPaymentByCodeWhenNoCandidateIsKnown(): void
    {
        $this->persistPaymentWithCode('AB12');
        $this->entityManager->flush();

        self::assertNull($this->matcher->matchByPaymentCode($this->transferTitled('Wpłata bez kodu')));
        self::assertNull($this->matcher->matchByPaymentCode($this->transferTitled('')));
        self::assertNull($this->matcher->matchByPaymentCode($this->transferTitled('!!! ---')));
    }

    private function persistPaymentWithCode(string $code): Payment
    {
        $user = UserAssembler::new()
            ->withName('Rodzic ' . $code)
            ->withEmail(strtolower($code) . '@example.com')
            ->assemble();
        $this->entityManager->persist($user);

        $payment = PaymentAssembler::new()
            ->withUser($user)
            ->withAmount(Money::of('100.00', 'PLN'))
            ->withStatus(Payment::STATUS_PENDING)
            ->assemble();
        $this->entityManager->persist($payment);

        $this->entityManager->persist(PaymentCodeAssembler::new()->withPayment($payment)->withCode($code)->assemble());

        return $payment;
    }

    private function transferTitled(string $title): Transfer
    {
        return TransferAssembler::new()
            ->withTitle($title)
            ->assemble();
    }
}
