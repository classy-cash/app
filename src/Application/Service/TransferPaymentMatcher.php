<?php

declare(strict_types=1);

namespace App\Application\Service;

use App\Entity\ClassCouncil\Student;
use App\Entity\Payment;
use App\Entity\PaymentCode;
use App\Entity\Transfer;
use App\Entity\User;
use App\Repository\ClassCouncil\StudentPaymentRepository;
use App\Repository\PaymentRepository;
use App\Repository\TransferRepository;
use Doctrine\ORM\EntityManagerInterface;

final readonly class TransferPaymentMatcher
{
    private const int SCORE_STUDENT_MATCH = 200;

    private const int SCORE_USER_MATCH = 100;

    private const int SCORE_AMOUNT_MATCH = 50;

    public function __construct(
        private EntityManagerInterface $entityManager,
        private TransferRepository $transferRepository,
        private PaymentRepository $paymentRepository,
        private StudentPaymentRepository $studentPaymentRepository,
    ) {}

    public function matchByPaymentCode(Transfer $transfer): ?Payment
    {
        // Keep the candidate order produced by the tokenizer (the first matching candidate wins),
        // but resolve all of them with a single query instead of one query per candidate.
        $candidates = array_values(array_unique(iterator_to_array($this->tokenizeTitle($transfer->title), false)));
        if ($candidates === []) {
            return null;
        }

        /** @var array<string, PaymentCode> $paymentCodes */
        $paymentCodes = [];
        foreach ($this->entityManager->getRepository(PaymentCode::class)->findBy([
            'code' => $candidates,
        ]) as $paymentCode) {
            $paymentCodes[$paymentCode->getCode()] = $paymentCode;
        }

        foreach ($candidates as $candidate) {
            if (isset($paymentCodes[$candidate])) {
                return $paymentCodes[$candidate]->getPayment();
            }
        }

        return null;
    }

    public function matchByHistoricalData(Transfer $transfer): ?Payment
    {
        $payments = $this->paymentRepository->findAssignableWithSearch('');
        $hints = $this->buildMatchHints($transfer, $payments);

        $studentCandidates = array_values(array_filter(
            $payments,
            static function (Payment $payment) use ($hints, $transfer): bool {
                $hint = $hints[$payment->getId()->toRfc4122()] ?? null;

                return $hint !== null
                    && $hint->hasStudentMatch()
                    && $payment->amountMatch($transfer);
            }
        ));

        if (count($studentCandidates) === 1) {
            return $studentCandidates[0];
        }

        $userCandidates = array_values(array_filter(
            $payments,
            static function (Payment $payment) use ($hints, $transfer): bool {
                $hint = $hints[$payment->getId()->toRfc4122()] ?? null;

                return $hint !== null
                    && ! $hint->hasStudentMatch()
                    && $hint->score >= self::SCORE_USER_MATCH
                    && $payment->amountMatch($transfer);
            }
        ));

        if (count($userCandidates) === 1) {
            return $userCandidates[0];
        }

        return null;
    }

    /**
     * @param Payment[] $payments
     * @return array{payments: Payment[], hints: array<string, TransferPaymentMatchHint>}
     */
    public function enrichAssignablePayments(Transfer $transfer, array $payments): array
    {
        $hints = $this->buildMatchHints($transfer, $payments);

        usort(
            $payments,
            static function (Payment $left, Payment $right) use ($hints): int {
                $leftScore = $hints[$left->getId()->toRfc4122()]->score ?? 0;
                $rightScore = $hints[$right->getId()->toRfc4122()]->score ?? 0;

                if ($leftScore !== $rightScore) {
                    return $rightScore <=> $leftScore;
                }

                return $right->getCreatedAt() <=> $left->getCreatedAt();
            }
        );

        return [
            'payments' => $payments,
            'hints' => $hints,
        ];
    }

    /**
     * @param Payment[] $payments
     * @return array<string, TransferPaymentMatchHint>
     */
    public function buildMatchHints(Transfer $transfer, array $payments): array
    {
        $context = $this->buildHistoricalContext($transfer);
        $hints = [];

        foreach ($payments as $payment) {
            $matchedStudentNames = [];
            $score = 0;

            foreach ($this->studentPaymentRepository->findByPayment($payment) as $studentPayment) {
                $student = $studentPayment->getStudent();
                $studentKey = $student->getId()
                    ->toRfc4122();

                if (isset($context['studentNames'][$studentKey])) {
                    $matchedStudentNames[] = $context['studentNames'][$studentKey];
                    $score += self::SCORE_STUDENT_MATCH;
                }
            }

            $userId = $this->userId($payment->getUser());
            if ($userId !== null && isset($context['userIds'][$userId])) {
                $score += self::SCORE_USER_MATCH;
            }

            if ($payment->amountMatch($transfer)) {
                $score += self::SCORE_AMOUNT_MATCH;
            }

            if ($score === 0) {
                continue;
            }

            $hints[$payment->getId()->toRfc4122()] = new TransferPaymentMatchHint(
                studentNames: array_values(array_unique($matchedStudentNames)),
                sameAccountNumber: $context['sameAccountNumber'],
                sameSender: $context['sameSender'],
                historicalMatchCount: $context['matchCount'],
                score: $score,
            );
        }

        return $hints;
    }

    /**
     * @return array{
     *     studentNames: array<string, string>,
     *     userIds: array<int, true>,
     *     sameAccountNumber: bool,
     *     sameSender: bool,
     *     matchCount: int
     * }
     */
    private function buildHistoricalContext(Transfer $transfer): array
    {
        $historicalTransfers = $this->transferRepository->findHistoricallyMatchedBySenderOrAccount(
            $transfer->getAccountNumber(),
            $transfer->getSender(),
            $transfer->getId(),
        );

        $studentNames = [];
        $userIds = [];
        $sameAccountNumber = false;
        $sameSender = false;

        foreach ($historicalTransfers as $historicalTransfer) {
            if ($historicalTransfer->getAccountNumber() === $transfer->getAccountNumber()) {
                $sameAccountNumber = true;
            }

            if ($historicalTransfer->getSender() === $transfer->getSender()) {
                $sameSender = true;
            }

            $payment = $historicalTransfer->getPayment();
            if ($payment === null) {
                continue;
            }

            $userId = $this->userId($payment->getUser());
            if ($userId !== null) {
                $userIds[$userId] = true;
            }

            foreach ($this->studentPaymentRepository->findByPayment($payment) as $studentPayment) {
                $student = $studentPayment->getStudent();
                $studentNames[$student->getId()->toRfc4122()] = $this->formatStudentName($student);
            }
        }

        return [
            'studentNames' => $studentNames,
            'userIds' => $userIds,
            'sameAccountNumber' => $sameAccountNumber,
            'sameSender' => $sameSender,
            'matchCount' => count($historicalTransfers),
        ];
    }

    private function formatStudentName(Student $student): string
    {
        return trim(sprintf('%s %s', $student->getFirstName(), $student->getLastName()));
    }

    private function userId(User $user): ?int
    {
        return $user->getId();
    }

    /**
     * @return \Generator<int, string>
     */
    private function tokenizeTitle(string $title): \Generator
    {
        $tokens = array_values(array_filter(
            explode(' ', preg_replace('/[^A-Za-z0-9]/', ' ', mb_strtoupper($title)) ?? ''),
            fn(string $word): bool => $word !== ''
        ));

        $emitted = [];

        foreach ($tokens as $token) {
            yield $emitted[] = $token;
        }

        $count = count($tokens);

        for ($i = 0; $i < $count - 1; $i++) {
            yield $emitted[] = $tokens[$i] . $tokens[$i + 1];
        }

        foreach ($emitted as $token) {
            $substituted = str_replace('0', 'O', $token);

            if ($substituted !== $token) {
                yield $substituted;
            }

            $substituted = str_replace('O', '0', $token);

            if ($substituted !== $token) {
                yield $substituted;
            }

            $substituted = strtr($token, [
                '0' => 'O',
                'O' => '0',
            ]);

            if ($substituted !== $token) {
                yield $substituted;
            }
        }
    }
}
